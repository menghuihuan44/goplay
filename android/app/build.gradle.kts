import java.util.Properties

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.serialization")
}

// --- Release signing -------------------------------------------------------
// A release APK is only installable if it is signed. Real release keys must
// never be committed, so signing material is read, in order, from:
//   1. environment variables,
//   2. android/keystore.properties (gitignored, optional),
//   3. Gradle properties (for example ~/.gradle/gradle.properties).
// Keys: GOPLAY_KEYSTORE, GOPLAY_KEYSTORE_PASSWORD, GOPLAY_KEY_ALIAS,
//       GOPLAY_KEY_PASSWORD
// When none of them is set the release build falls back to the debug signing
// config so that a locally built release APK can still be installed. That
// fallback is NOT suitable for publishing to an app store.
val signingPropNames = listOf(
    "GOPLAY_KEYSTORE",
    "GOPLAY_KEYSTORE_PASSWORD",
    "GOPLAY_KEY_ALIAS",
    "GOPLAY_KEY_PASSWORD",
)

val localSigningProps = Properties().apply {
    val f = rootProject.file("keystore.properties")
    if (f.isFile) f.inputStream().use { load(it) }
}

fun signingValue(name: String): String? =
    System.getenv(name)?.takeIf { it.isNotBlank() }
        ?: localSigningProps.getProperty(name)?.takeIf { it.isNotBlank() }
        ?: (project.findProperty(name) as String?)?.takeIf { it.isNotBlank() }

val signingValues = signingPropNames.associateWith { signingValue(it) }
val providedSigningKeys = signingValues.filterValues { it != null }.keys
val hasReleaseSigning = providedSigningKeys.size == signingPropNames.size

if (providedSigningKeys.isNotEmpty() && !hasReleaseSigning) {
    throw GradleException(
        "Incomplete release signing configuration: provide all of " +
            signingPropNames.joinToString(", ") +
            " (missing: " + (signingPropNames - providedSigningKeys).joinToString(", ") + ")."
    )
}

val releaseKeystoreFile: File? = signingValues["GOPLAY_KEYSTORE"]?.let { raw ->
    val f = File(raw)
    val resolved = if (f.isAbsolute) f else rootProject.file(raw)
    if (!resolved.isFile) {
        throw GradleException("GOPLAY_KEYSTORE points to a missing file: " + resolved.absolutePath)
    }
    resolved
}

android {
    namespace = "com.goplay.mobile"
    compileSdk = 35
    // Pin the build-tools that are installed locally; AGP 8.7 would otherwise
    // fetch its default 34.0.0 on first build.
    buildToolsVersion = "35.0.0"

    defaultConfig {
        applicationId = "com.goplay.mobile"
        minSdk = 26
        targetSdk = 35
        versionCode = 9
        versionName = "0.4.5"
        ndk {
            // The Go core and llama-server ship as prebuilt arm64 libraries.
            // Go's android/amd64 target needs cgo + the NDK, so x86_64
            // emulators are intentionally not packaged in v0.4.
            abiFilters += listOf("arm64-v8a")
        }
    }

    signingConfigs {
        if (hasReleaseSigning) {
            create("release") {
                storeFile = releaseKeystoreFile
                storePassword = signingValues["GOPLAY_KEYSTORE_PASSWORD"]
                keyAlias = signingValues["GOPLAY_KEY_ALIAS"]
                keyPassword = signingValues["GOPLAY_KEY_PASSWORD"]
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro"
            )
            signingConfig =
                if (hasReleaseSigning) signingConfigs.getByName("release")
                else signingConfigs.getByName("debug")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    buildFeatures {
        buildConfig = false
    }

    packaging {
        resources.excludes += "/META-INF/{AL2.0,LGPL2.1}"
        // extractNativeLibs=true keeps the Go core executable from
        // applicationInfo.nativeLibraryDir on all supported Android versions.
        jniLibs.useLegacyPackaging = true
    }

    lint {
        abortOnError = false
        checkReleaseBuilds = false
    }
}

if (!hasReleaseSigning) {
    logger.lifecycle(
        "GoPlay: GOPLAY_KEYSTORE* not set; release APK will be signed with the " +
            "debug keystore (installable locally, not for store publishing)."
    )
}

dependencies {
    implementation("androidx.core:core-ktx:1.15.0")
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("androidx.activity:activity-ktx:1.9.3")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.8.7")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.9.0")
    implementation("org.jetbrains.kotlinx:kotlinx-serialization-json:1.7.3")
    implementation("com.squareup.okhttp3:okhttp:4.12.0")

    testImplementation("junit:junit:4.13.2")
}
