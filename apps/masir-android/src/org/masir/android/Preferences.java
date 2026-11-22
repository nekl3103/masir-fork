package org.masir.android;

import android.content.Context;
import android.content.SharedPreferences;
import android.content.res.Configuration;
import java.util.Locale;

final class Preferences {
    private Preferences() {}

    static SharedPreferences get(Context context) {
        return context.getSharedPreferences("masir", Context.MODE_PRIVATE);
    }

    static Context localized(Context context) {
        String language = get(context).getString("language", Locale.getDefault().getLanguage());
        Configuration config = new Configuration(context.getResources().getConfiguration());
        config.setLocale(Locale.forLanguageTag(language.equals("ru") ? "ru" : "en"));
        return context.createConfigurationContext(config);
    }
}
