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
        versionCode = 1
        versionName = "1.0.0"
        // Default server shown on the setup screen. Change before building,
        // or type the URL on the TV.
        buildConfigField("String", "DEFAULT_SERVER", "\"https://YOUR-APP.vercel.app\"")
    }

    buildFeatures {
        buildConfig = true
    }

    buildTypes {
        release {
            isMinifyEnabled = false
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}
