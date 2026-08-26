# Flutter ProGuard Rules
-keep class io.flutter.app.** { *; }
-keep class io.flutter.plugin.** { *; }
-keep class io.flutter.util.** { *; }
-keep class io.flutter.view.** { *; }
-keep class io.flutter.** { *; }
-keep class io.flutter.plugins.** { *; }

# Google Play Core & Deferred Components
-dontwarn com.google.android.play.core.**
-dontwarn io.flutter.embedding.engine.deferredcomponents.**

# Stripe SDK
-dontwarn com.stripe.android.pushProvisioning.**
-dontwarn com.stripe.android.**
-dontwarn com.reactnativestripesdk.**

# General
-dontwarn javax.annotation.**
-dontwarn org.jetbrains.kotlin.**
-dontwarn okhttp3.**
-dontwarn okio.**
