import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

import 'package:omnigo_app/main.dart';

void main() {
  testWidgets('Omnigo App successful boot smoke test', (WidgetTester tester) async {
    // Set a realistic screen size so complex UI elements don't overflow
    tester.view.physicalSize = const Size(1080, 2400);
    tester.view.devicePixelRatio = 3.0;

    // Build our app and trigger a frame.
    await tester.pumpWidget(const OmnigoApp());
    await tester.pumpAndSettle();

    // Verify the app rendered successfully and we can find standard Material elements
    expect(find.byType(MaterialApp), findsOneWidget);
    expect(find.byType(Scaffold), findsWidgets);

    // Reset view configuration after test
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
  });
}
