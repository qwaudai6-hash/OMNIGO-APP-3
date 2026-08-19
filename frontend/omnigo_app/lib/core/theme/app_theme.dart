import 'package:flutter/material.dart';

class AppTheme {
  // Colors inspired by the Dribbble screenshot
  static const Color bgColor = Color(0xFFF8F9FA); // Off-white/Light grey
  static const Color blackAccent = Color(0xFF111111); // Deep black for buttons/text
  static const Color limeAccent = Color(0xFFC7F464); // The bright green in the pill
  static const Color softPink = Color(0xFFFCE4EC); // Soft pink behind the shoe
  static const Color softBlue = Color(0xFFE3F2FD); 
  static const Color softGreen = Color(0xFFE8F5E9);

  static final ThemeData lightTheme = ThemeData(
    brightness: Brightness.light,
    scaffoldBackgroundColor: bgColor,
    primaryColor: blackAccent,
    colorScheme: const ColorScheme.light(
      primary: blackAccent,
      secondary: limeAccent,
      surface: Colors.white,
      surfaceContainer: bgColor,
    ),
    fontFamily: 'Inter', // Clean sans-serif
    textTheme: const TextTheme(
      displayLarge: TextStyle(fontSize: 32, fontWeight: FontWeight.w800, color: blackAccent, letterSpacing: -0.5),
      displayMedium: TextStyle(fontSize: 24, fontWeight: FontWeight.bold, color: blackAccent),
      bodyLarge: TextStyle(fontSize: 16, color: Color(0xFF4A4A4A)),
      bodyMedium: TextStyle(fontSize: 14, color: Color(0xFF757575)),
    ),
    elevatedButtonTheme: ElevatedButtonThemeData(
      style: ElevatedButton.styleFrom(
        backgroundColor: blackAccent,
        foregroundColor: Colors.white,
        padding: const EdgeInsets.symmetric(vertical: 18, horizontal: 32),
        shape: RoundedRectangleBorder(
          borderRadius: BorderRadius.circular(30), // Pill shape
        ),
        elevation: 0,
      ),
    ),
    inputDecorationTheme: InputDecorationTheme(
      filled: true,
      fillColor: Colors.white,
      hintStyle: const TextStyle(color: Color(0xFF9E9E9E)),
      border: OutlineInputBorder(
        borderRadius: BorderRadius.circular(20),
        borderSide: BorderSide.none,
      ),
      enabledBorder: OutlineInputBorder(
        borderRadius: BorderRadius.circular(20),
        borderSide: const BorderSide(color: Color(0xFFEEEEEE), width: 1.5),
      ),
      focusedBorder: OutlineInputBorder(
        borderRadius: BorderRadius.circular(20),
        borderSide: const BorderSide(color: blackAccent, width: 2),
      ),
      contentPadding: const EdgeInsets.symmetric(horizontal: 20, vertical: 20),
    ),
  );
}
