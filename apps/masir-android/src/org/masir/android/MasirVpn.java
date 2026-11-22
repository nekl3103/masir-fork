package org.masir.android;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.net.VpnService;
import android.os.Build;
import android.os.Handler;
import android.os.ParcelFileDescriptor;
import java.io.File;
import java.io.FileOutputStream;
import java.io.InputStream;
import java.io.IOException;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import org.masir.core.mobile.Mobile;

public final class MasirVpn extends VpnService {
    static final String STOP = "org.masir.android.STOP";
    static volatile int state;
    static volatile boolean alwaysOn;
    static volatile String error = "";
    private static final ExecutorService worker = Executors.newSingleThreadExecutor();
    private static final String CHANNEL = "vpn";
    private final Handler main = new Handler(android.os.Looper.getMainLooper());
    private ParcelFileDescriptor tunnel;
    private volatile boolean destroyed;

    @Override
    protected void attachBaseContext(Context context) {
        super.attachBaseContext(Preferences.localized(context));
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        if (intent != null && STOP.equals(intent.getAction())) {
            if (!alwaysOn) stopSelf(startId);
            return START_NOT_STICKY;
        }
        if (state != 0) return START_STICKY;
        alwaysOn = Build.VERSION.SDK_INT >= 29 && isAlwaysOn();
        state = 1;
        error = "";
        notification(R.string.starting);
        worker.execute(() -> {
            try {
                SharedPreferences prefs = Preferences.get(this);
                String profile = prefs.getString("selected", "");
                if (profile.isEmpty()) throw new IOException(getString(R.string.no_profiles));
                File assets = prepareAssets();
                if (destroyed) return;
                tunnel = new Builder().setSession(getString(R.string.app_name))
                    .setMtu(1500)
                    .addAddress("10.10.20.1", 24)
                    .addAddress("fd00::1", 64)
                    .addRoute("0.0.0.0", 0)
                    .addRoute("::", 0)
                    .addDnsServer("1.1.1.1")
                    .establish();
                if (tunnel == null) throw new IOException(getString(R.string.permission_denied));
                Mobile.start(tunnel.getFd(), profile, prefs.getString("direct", ""),
                    assets.getAbsolutePath(), prefs.getBoolean("strict", true), this::protect);
                main.post(() -> {
                    if (!destroyed) {
                        state = 2;
                        notification(R.string.connected);
                    }
                });
            } catch (Exception failure) {
                main.post(() -> {
                    if (!destroyed) {
                        error = failure.getMessage() == null ? failure.getClass().getSimpleName() : failure.getMessage();
                        stopSelf(startId);
                    }
                });
            }
        });
        return START_STICKY;
    }

    private File prepareAssets() throws IOException {
        String version;
        try {
            version = getPackageManager().getPackageInfo(getPackageName(), 0).versionName;
        } catch (android.content.pm.PackageManager.NameNotFoundException e) {
            throw new IOException(e);
        }
        File directory = new File(getFilesDir(), "geo-" + version);
        if (!directory.isDirectory() && !directory.mkdirs()) throw new IOException("Could not prepare VPN assets");
        for (String name : new String[]{"geoip.dat", "geosite.dat"}) {
            File ready = new File(directory, name);
            if (ready.isFile()) continue;
            File temporary = new File(directory, name + ".part");
            try (InputStream in = getAssets().open(name); FileOutputStream out = new FileOutputStream(temporary)) {
                byte[] buffer = new byte[65536];
                for (int n; (n = in.read(buffer)) != -1; ) out.write(buffer, 0, n);
            }
            if (!temporary.renameTo(ready)) throw new IOException("Could not save VPN assets");
        }
        return directory;
    }

    private void notification(int message) {
        NotificationManager manager = getSystemService(NotificationManager.class);
        manager.createNotificationChannel(new NotificationChannel(CHANNEL, getString(R.string.app_name), NotificationManager.IMPORTANCE_LOW));
        Intent open = new Intent(this, MainActivity.class);
        PendingIntent pending = PendingIntent.getActivity(this, 0, open, PendingIntent.FLAG_IMMUTABLE);
        Notification n = new Notification.Builder(this, CHANNEL)
            .setSmallIcon(R.drawable.masir_icon).setContentTitle(getString(R.string.app_name))
            .setContentText(getString(message)).setContentIntent(pending).setOngoing(true).build();
        startForeground(1, n);
    }

    @Override
    public void onRevoke() {
        stopSelf();
    }

    @Override
    public void onDestroy() {
        destroyed = true;
        state = 0;
        alwaysOn = false;
        worker.execute(() -> {
            Mobile.stop();
            if (tunnel != null) {
                try { tunnel.close(); } catch (IOException ignored) { }
                tunnel = null;
            }
        });
        stopForeground(STOP_FOREGROUND_REMOVE);
        super.onDestroy();
    }
}
