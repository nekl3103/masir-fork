#!/bin/sh
# Build an installable arm64/x86_64 APK using the Android SDK, NDK and Go.
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
sdk=${ANDROID_HOME:-${ANDROID_SDK_ROOT:-}}
if [ -z "$sdk" ]; then
    echo 'Set ANDROID_HOME to the Android SDK.' >&2
    exit 1
fi
tools="$sdk/build-tools/35.0.0"
platform="$sdk/platforms/android-35/android.jar"
export ANDROID_NDK_HOME=${ANDROID_NDK_HOME:-"$sdk/ndk/28.2.13676358"}
for required in "$platform" "$tools/aapt2" "$tools/d8" "$tools/zipalign" "$tools/apksigner" "$ANDROID_NDK_HOME/source.properties"; do
    if [ ! -f "$required" ]; then
        echo "Android SDK component missing: $required" >&2
        exit 1
    fi
done
for program in java javac jar keytool zip; do
    if ! command -v "$program" >/dev/null; then
        echo "Required Android build tool missing: $program" >&2
        exit 1
    fi
done
java -version

version=${ANDROID_VERSION:-$(sed -n 's/.*"version": *"\([0-9][0-9.]*\)".*/\1/p' apps/masir-gui/mygo.json)}
version_code=$(printf '%s' "$version" | awk -F. 'NF == 3 && /^[0-9]+\.[0-9]+\.[0-9]+$/ { printf "%.0f", $1*1000000+$2*1000+$3 }')
if [ -z "$version_code" ] || [ "$version_code" -lt 1 ]; then
    echo "Invalid Android version: $version" >&2
    exit 1
fi
if [ "${MASIR_GEO_SKIP:-0}" != 1 ]; then
    sh scripts/fetch-geo.sh
fi
for data in geoip.dat geosite.dat; do
    test -s "build/geo/$data"
done

mkdir -p build/android dist/android
app="$root/apps/masir-android"
scratch=$(mktemp -d "$root/build/android/apk.XXXXXX")
trap 'rm -rf "$scratch"' EXIT
trap 'exit 1' HUP INT TERM

host=$(go env GOHOSTOS)
case "$host" in
    darwin|linux) ndk_bin="$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/$host-x86_64/bin" ;;
    *) echo "Unsupported Android build host: $host" >&2; exit 1 ;;
esac
sh scripts/with-xray.sh sh -eu -c '
    app=$1; scratch=$2; ndk_bin=$3
    go work use "$app"
    go build -trimpath -o "$scratch/gobind" golang.org/x/mobile/cmd/gobind
    GOOS=android GOARCH=arm64 CGO_ENABLED=0 "$scratch/gobind" -lang=go,java -javapkg=org.masir.core \
        -outdir="$scratch/core" github.com/Gozargah/Masir/libs/masir-daemon/mobile
    go -C "$scratch/core/src" mod init masir-android-bindings
    go work use "$scratch/core/src"
    for arch in arm64 amd64; do
        case "$arch" in
            arm64) abi=arm64-v8a; triple=aarch64-linux-android ;;
            amd64) abi=x86_64; triple=x86_64-linux-android ;;
        esac
        mkdir -p "$scratch/lib/$abi"
        # anet uses Go’s IPv6 zone cache on Android via linkname.
        GOOS=android GOARCH="$arch" CGO_ENABLED=1 CC="$ndk_bin/${triple}26-clang" \
            go -C "$scratch/core/src" build -trimpath -buildmode=c-shared -ldflags="-s -w -checklinkname=0" \
            -o "$scratch/lib/$abi/libgojni.so" ./gobind
        rm -f "$scratch/lib/$abi/libgojni.h"
    done
' sh "$app" "$scratch" "$ndk_bin"

mkdir -p "$scratch/java" "$scratch/classes" "$scratch/dex"
"$tools/aapt2" compile --dir "$app/res" -o "$scratch/resources.zip"
"$tools/aapt2" link -I "$platform" --manifest "$app/AndroidManifest.xml" \
    --min-sdk-version 26 --target-sdk-version 35 --version-code "$version_code" --version-name "$version" \
    --java "$scratch/java" -A "$root/build/geo" -o "$scratch/app.apk" "$scratch/resources.zip"
javac --release 8 -classpath "$platform" -d "$scratch/classes" \
    "$scratch/java/org/masir/android/R.java" "$app"/src/org/masir/android/*.java \
    "$scratch/core"/java/go/*.java "$scratch/core"/java/org/masir/core/mobile/*.java
jar cf "$scratch/java.jar" -C "$scratch/classes" .
"$tools/d8" --lib "$platform" --min-api 26 --output "$scratch/dex" "$scratch/java.jar"
(cd "$scratch/dex" && zip -q "$scratch/app.apk" ./*.dex)
(cd "$scratch" && zip -0 -qr "$scratch/app.apk" lib)
"$tools/zipalign" -P 16 -f 4 "$scratch/app.apk" "$scratch/aligned.apk"

export MASIR_ANDROID_KEY_PASSWORD=${MASIR_ANDROID_KEY_PASSWORD:-android}
export MASIR_ANDROID_STORE_PASSWORD=${MASIR_ANDROID_STORE_PASSWORD:-android}
keystore=${MASIR_ANDROID_KEYSTORE:-"$root/build/android/test-signing.jks"}
alias=${MASIR_ANDROID_KEY_ALIAS:-androiddebugkey}
if [ -z "${MASIR_ANDROID_KEYSTORE:-}" ] && [ ! -f "$keystore" ]; then
    keytool -genkeypair -noprompt -keystore "$keystore" -storetype JKS -storepass:env MASIR_ANDROID_STORE_PASSWORD \
        -keypass:env MASIR_ANDROID_KEY_PASSWORD -alias "$alias" -dname 'CN=Masir Android Test' -keyalg RSA -keysize 2048 -validity 10000
fi
output="$root/dist/android/masir-android-$version.apk"
"$tools/apksigner" sign --ks "$keystore" --ks-key-alias "$alias" --ks-pass env:MASIR_ANDROID_STORE_PASSWORD \
    --key-pass env:MASIR_ANDROID_KEY_PASSWORD --out "$scratch/signed.apk" "$scratch/aligned.apk"
"$tools/apksigner" verify "$scratch/signed.apk"
mv "$scratch/signed.apk" "$output"
printf 'Built %s\n' "$output"
