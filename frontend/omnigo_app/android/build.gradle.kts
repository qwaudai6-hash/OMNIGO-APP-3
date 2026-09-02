allprojects {
    repositories {
        google()
        mavenCentral()
    }
    configurations.all {
        resolutionStrategy {
            force("androidx.core:core:1.15.0")
            force("androidx.core:core-ktx:1.15.0")
            force("org.jetbrains.kotlin:kotlin-stdlib:1.9.25")
            force("org.jetbrains.kotlin:kotlin-stdlib-jdk7:1.9.25")
            force("org.jetbrains.kotlin:kotlin-stdlib-jdk8:1.9.25")
            force("org.jetbrains.kotlinx:kotlinx-coroutines-core:1.8.1")
            force("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.8.1")
            force("com.squareup.okio:okio-jvm:3.9.1")
            force("com.squareup.okhttp3:okhttp:4.12.0")
            force("com.squareup.okhttp3:okhttp-urlconnection:4.12.0")
            force("com.squareup.okhttp3:logging-interceptor:4.12.0")
            force("com.squareup.okhttp3:okhttp-dns:4.12.0")
        }
    }
}

val newBuildDir: Directory =
    rootProject.layout.buildDirectory
        .dir("../../build")
        .get()
rootProject.layout.buildDirectory.value(newBuildDir)

subprojects {
    val newSubprojectBuildDir: Directory = newBuildDir.dir(project.name)
    project.layout.buildDirectory.value(newSubprojectBuildDir)
}
subprojects {
    project.evaluationDependsOn(":app")
    configurations.all {
        resolutionStrategy {
            force("androidx.core:core:1.15.0")
            force("androidx.core:core-ktx:1.15.0")
            force("org.jetbrains.kotlin:kotlin-stdlib:1.9.25")
            force("org.jetbrains.kotlin:kotlin-stdlib-jdk7:1.9.25")
            force("org.jetbrains.kotlin:kotlin-stdlib-jdk8:1.9.25")
            force("org.jetbrains.kotlinx:kotlinx-coroutines-core:1.8.1")
            force("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.8.1")
            force("com.squareup.okio:okio-jvm:3.9.1")
            force("com.squareup.okhttp3:okhttp:4.12.0")
            force("com.squareup.okhttp3:okhttp-urlconnection:4.12.0")
            force("com.squareup.okhttp3:logging-interceptor:4.12.0")
            force("com.squareup.okhttp3:okhttp-dns:4.12.0")
        }
    }
}

subprojects {
    tasks.withType<org.jetbrains.kotlin.gradle.tasks.KotlinCompile>().configureEach {
        compilerOptions {
            languageVersion.set(org.jetbrains.kotlin.gradle.dsl.KotlinVersion.KOTLIN_1_9)
            apiVersion.set(org.jetbrains.kotlin.gradle.dsl.KotlinVersion.KOTLIN_1_9)
            jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17)
        }
    }
    tasks.withType<JavaCompile>().configureEach {
        sourceCompatibility = "17"
        targetCompatibility = "17"
    }
}

tasks.register<Delete>("clean") {
    delete(rootProject.layout.buildDirectory)
}
