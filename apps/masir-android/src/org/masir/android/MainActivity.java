package org.masir.android;

import android.app.Activity;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.net.VpnService;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.provider.Settings;
import android.text.InputType;
import android.view.View;
import android.widget.AdapterView;
import android.widget.ArrayAdapter;
import android.widget.Button;
import android.widget.CheckBox;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.Spinner;
import android.widget.TextView;
import android.widget.Toast;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.Map;
import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;
import org.masir.core.mobile.Mobile;

public final class MainActivity extends Activity {
    private static final int VPN_PERMISSION = 1;
    private final Handler main = new Handler(Looper.getMainLooper());
    private SharedPreferences prefs;
    private JSONArray profiles;
    private Spinner nodes;
    private EditText source;
    private EditText direct;
    private CheckBox strict;
    private TextView status;
    private Button connection;
    private Button clear;
    private Button importButton;
    private boolean importing;
    private final Runnable update = new Runnable() {
        @Override public void run() {
            renderStatus();
            main.postDelayed(this, 400);
        }
    };

    @Override protected void attachBaseContext(Context context) {
        super.attachBaseContext(Preferences.localized(context));
    }

    @Override public void onCreate(Bundle bundle) {
        super.onCreate(bundle);
        prefs = Preferences.get(this);
        try { profiles = new JSONArray(prefs.getString("profiles", "[]")); }
        catch (JSONException ignored) { profiles = new JSONArray(); }

        ScrollView scroll = new ScrollView(this);
        LinearLayout page = new LinearLayout(this);
        page.setOrientation(LinearLayout.VERTICAL);
        page.setPadding(dp(20), dp(20), dp(20), dp(24));
        scroll.addView(page);
        setContentView(scroll);

        TextView title = text(page, R.string.app_name);
        title.setTextSize(28);
        status = text(page, R.string.disconnected);
        status.setTextSize(16);
        connection = button(page, R.string.connect, this::toggleVpn);
        nodes = new Spinner(this);
        page.addView(nodes);
        populateNodes();

        source = new EditText(this);
        source.setHint(R.string.source);
        source.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_FLAG_MULTI_LINE | InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS);
        source.setMinLines(2);
        source.setSaveEnabled(false); // Share links can contain credentials.
        page.addView(source);
        importButton = button(page, R.string.import_profiles, this::importProfiles);
        clear = button(page, R.string.clear_profiles, () -> {
            profiles = new JSONArray();
            prefs.edit().remove("profiles").remove("selected").apply();
            populateNodes();
            renderStatus();
        });

        text(page, R.string.direct);
        direct = new EditText(this);
        direct.setText(prefs.getString("direct", ""));
        direct.setHint(R.string.direct_example);
        direct.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_FLAG_MULTI_LINE | InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS);
        page.addView(direct);
        strict = new CheckBox(this);
        strict.setText(R.string.kill_switch);
        strict.setChecked(prefs.getBoolean("strict", true));
        strict.setOnCheckedChangeListener((view, checked) -> prefs.edit().putBoolean("strict", checked).apply());
        page.addView(strict);
        button(page, R.string.system_vpn, () -> startActivity(new Intent(Settings.ACTION_VPN_SETTINGS)));
        text(page, R.string.system_hint);
        configureLanguage(page);
        String version = "";
        try { version = getPackageManager().getPackageInfo(getPackageName(), 0).versionName; }
        catch (android.content.pm.PackageManager.NameNotFoundException ignored) { }
        TextView versions = new TextView(this);
        versions.setText(getString(R.string.version, version, Mobile.amneziaWGVersion()));
        page.addView(versions);
        button(page, R.string.quit, () -> {
            if (MasirVpn.state != 0 && !MasirVpn.alwaysOn) {
                startService(new Intent(this, MasirVpn.class).setAction(MasirVpn.STOP));
            }
            finish();
        });
        renderStatus();
    }

    private void configureLanguage(LinearLayout page) {
        text(page, R.string.language);
        Spinner languages = new Spinner(this);
        languages.setAdapter(adapter(new String[]{"English", "Русский"}));
        int selected = getResources().getConfiguration().getLocales().get(0).getLanguage().equals("ru") ? 1 : 0;
        languages.setSelection(selected);
        languages.setOnItemSelectedListener(new AdapterView.OnItemSelectedListener() {
            @Override public void onItemSelected(AdapterView<?> parent, View view, int index, long id) {
                if (index != selected) {
                    prefs.edit().putString("language", index == 1 ? "ru" : "en").apply();
                    recreate();
                }
            }
            @Override public void onNothingSelected(AdapterView<?> parent) { }
        });
        page.addView(languages);
    }

    @Override protected void onResume() {
        super.onResume();
        main.post(update);
    }

    @Override protected void onPause() {
        main.removeCallbacks(update);
        if (direct != null) prefs.edit().putString("direct", direct.getText().toString()).apply();
        super.onPause();
    }

    private void populateNodes() {
        ArrayList<String> labels = new ArrayList<>();
        String current = prefs.getString("selected", "");
        int selected = 0;
        for (int i = 0; i < profiles.length(); i++) {
            JSONObject profile = profiles.optJSONObject(i);
            labels.add(profile.optString("name"));
            if (profile.toString().equals(current)) selected = i;
        }
        if (labels.isEmpty()) labels.add(getString(R.string.no_profiles));
        nodes.setAdapter(adapter(labels.toArray(new String[0])));
        nodes.setSelection(selected);
    }

    private void importProfiles() {
        String text = source.getText().toString().trim();
        if (text.isEmpty() || importing) return;
        importing = true;
        renderStatus();
        new Thread(() -> {
            try {
                JSONArray received = new JSONArray(Mobile.importProfiles(text));
                main.post(() -> {
                    Map<String, JSONObject> unique = new LinkedHashMap<>();
                    for (JSONArray group : new JSONArray[]{profiles, received}) {
                        for (int i = 0; i < group.length(); i++) {
                            JSONObject entry = group.optJSONObject(i);
                            unique.put(entry.optString("id"), entry);
                        }
                    }
                    profiles = new JSONArray(unique.values());
                    prefs.edit().putString("profiles", profiles.toString()).apply();
                    importing = false;
                    if (!isDestroyed()) {
                        source.setText("");
                        populateNodes();
                        renderStatus();
                    }
                });
            } catch (Exception failure) {
                main.post(() -> {
                    importing = false;
                    if (!isDestroyed()) {
                        renderStatus();
                        Toast.makeText(this, getString(R.string.error, failure.getMessage()), Toast.LENGTH_LONG).show();
                    }
                });
            }
        }, "masir-import").start();
    }

    private void toggleVpn() {
        if (MasirVpn.state != 0) {
            startService(new Intent(this, MasirVpn.class).setAction(MasirVpn.STOP));
            return;
        }
        JSONObject profile = profiles.optJSONObject(nodes.getSelectedItemPosition());
        if (profile == null) return;
        prefs.edit().putString("selected", profile.toString()).putString("direct", direct.getText().toString()).apply();
        Intent consent = VpnService.prepare(this);
        if (consent == null) startVpn();
        else startActivityForResult(consent, VPN_PERMISSION);
    }

    @Override protected void onActivityResult(int request, int result, Intent data) {
        super.onActivityResult(request, result, data);
        if (request != VPN_PERMISSION) return;
        if (result == RESULT_OK) startVpn();
        else Toast.makeText(this, R.string.permission_denied, Toast.LENGTH_LONG).show();
    }

    private void startVpn() { startForegroundService(new Intent(this, MasirVpn.class)); }

    private void renderStatus() {
        boolean active = MasirVpn.state != 0;
        status.setText(MasirVpn.error.isEmpty()
            ? getString(MasirVpn.state == 2 ? R.string.connected : active ? R.string.starting : R.string.disconnected)
            : getString(R.string.error, MasirVpn.error));
        connection.setText(active ? R.string.disconnect : R.string.connect);
        connection.setEnabled(active ? !MasirVpn.alwaysOn : profiles.length() > 0 && !importing);
        nodes.setEnabled(!active);
        direct.setEnabled(!active);
        strict.setEnabled(!active);
        clear.setEnabled(!active && !importing && profiles.length() > 0);
        importButton.setEnabled(!active && !importing);
        importButton.setText(importing ? R.string.importing : R.string.import_profiles);
    }

    private ArrayAdapter<String> adapter(String[] values) {
        ArrayAdapter<String> adapter = new ArrayAdapter<>(this, android.R.layout.simple_spinner_item, values);
        adapter.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item);
        return adapter;
    }

    private TextView text(LinearLayout view, int label) {
        TextView text = new TextView(this);
        text.setText(label);
        view.addView(text);
        return text;
    }

    private Button button(LinearLayout view, int label, Runnable action) {
        Button button = new Button(this);
        button.setText(label);
        button.setOnClickListener(v -> action.run());
        view.addView(button);
        return button;
    }

    private int dp(int value) { return Math.round(value * getResources().getDisplayMetrics().density); }
}
