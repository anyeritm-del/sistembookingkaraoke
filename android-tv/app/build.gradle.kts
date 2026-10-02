plugins {
    id("com.android.application")
}

android {
    namespace = "com.sentineltech.karaoketv"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.sentineltech.karaoketv"
        minSdk = 23
        targetSdk = 35
        versionCode = 2
        versionName = "1.1.0"
        // Default server shown on the setup screen; it can be changed on the TV.
        buildConfigField("String", "DEFAULT_SERVER", "\"https://sistembookingkaraoke.vercel.app\"")
    }

    // Release signing comes from environment variables, so the keystore and
    // its passwords never enter the repository. Keep the same keystore for
    // every release: Android only installs an update signed with the same key.
    signingConfigs {
        create("release") {
            val store = System.getenv("KARAOKE_TV_KEYSTORE")
            if (store != null) {
                storeFile = file(store)
                storePassword = System.getenv("KARAOKE_TV_STORE_PASSWORD")
                keyAlias = System.getenv("KARAOKE_TV_KEY_ALIAS") ?: "karaoke-tv"
                keyPassword = System.getenv("KARAOKE_TV_KEY_PASSWORD")
            }
        }
    }

    buildFeatures {
        buildConfig = true
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            if (System.getenv("KARAOKE_TV_KEYSTORE") != null) {
                signingConfig = signingConfigs.getByName("release")
            }
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}
