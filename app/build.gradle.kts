plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.goterm.app"
    compileSdk = 34

    defaultConfig {
        applicationId = "com.goterm.app"
        minSdk = 24
        targetSdk = 34
        versionCode = 1
        versionName = "1.11.0"
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
    kotlinOptions {
        jvmTarget = "17"
    }

    // The Go engine ships as libgoterm_engine.so and is started as a
    // process, so the installer must extract it to the native library dir.
    packaging {
        jniLibs {
            useLegacyPackaging = true
        }
    }
}
