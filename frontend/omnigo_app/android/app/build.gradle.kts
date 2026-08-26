import java.util.Properties
import java.io.FileInputStream

plugins {
    id("com.android.application")
    id("kotlin-android")
    id("dev.flutter.flutter-gradle-plugin")
}

// Release signing: reads android/key.properties (gitignored).
// CI override via env vars: OMNIGO_STORE_FILE / OMNIGO_STORE_PASSWORD /
// OMNIGO_KEY_ALIAS / OMNIGO_KEY_PASSWORD.
val keystoreProperties = Properties().apply {
    val f = rootProject.file("key.properties")
    if (f.exists()) FileInputStream(f).use { load(it) }
}
val storeFilePath = System.getenv("OMNIGO_STORE_FILE")
    ?: keystoreProperties.getProperty("storeFile")
val hasReleaseSigning = !storeFilePath.isNullOrBlank() &&
        file(storeFilePath).exists()

android {
    namespace = "com.omnigo.app"
    compileSdk = flutter.compileSdkVersion
    ndkVersion = "27.0.12077973"

    compileOptions {
        isCoreLibraryDesugaringEnabled = true
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    defaultConfig {
        applicationId = "com.omnigo.app"
        minSdk = 24
        targetSdk = flutter.targetSdkVersion
        versionCode = flutter.versionCode
        versionName = flutter.versionName
        multiDexEnabled = true
    }

    signingConfigs {
        if (hasReleaseSigning) {
            create("release") {
                storeFile = file(storeFilePath!!)
                storePassword = System.getenv("OMNIGO_STORE_PASSWORD")
                    ?: keystoreProperties.getProperty("storePassword")
                keyAlias = System.getenv("OMNIGO_KEY_ALIAS")
                    ?: keystoreProperties.getProperty("keyAlias")
                keyPassword = System.getenv("OMNIGO_KEY_PASSWORD")
                    ?: keystoreProperties.getProperty("keyPassword")
            }
        }
    }

    buildTypes {
        release {
            // Signed with the production release keystore when available;
            // falls back to debug ONLY for local unsigned dev builds so the
            // project still assembles on a fresh clone. CI release jobs MUST
            // provide key.properties (or env vars) or fail loudly instead of
            // shipping a debug-signed artifact.
            signingConfig = if (hasReleaseSigning) {
                signingConfigs.getByName("release")
            } else {
                logger.warn(
                    "!!! OMNIGO RELEASE BUILD IS DEBUG-SIGNED — " +
                    "provide android/key.properties for production !!!"
                )
                signingConfigs.getByName("debug")
            }
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro"
            )
        }
    }
}

dependencies {
    coreLibraryDesugaring("com.android.tools:desugar_jdk_libs:2.1.4")
}

flutter {
    source = "../.."
}
