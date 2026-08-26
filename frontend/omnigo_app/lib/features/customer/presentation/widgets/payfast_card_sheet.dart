import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:webview_flutter/webview_flutter.dart';

/// Shared PayFast "Option C" payment UI used by both the cart checkout flow
/// and the product-page Buy Now flow.
///
/// PCI-DSS note (CRITICAL-07 mitigation): raw card entry is scoped to the
/// PayFast Option C initiation only — the backend NEVER stores or logs
/// PAN/CVV (fields are json:"-" and redacted in String()).
/// Prefer the saved-card instrument-token flow once a card exists in the
/// vault; hosted-checkout redirect is the long-term SAQ-A target.

/// Shows the PayFast card-entry bottom sheet.
///
/// Returns a map with keys `card_number`, `expiry_month`, `expiry_year`,
/// `cvv` and `customer_mobile_no`, or null when the user cancelled.
Future<Map<String, String>?> showPayFastCardDetailsSheet(BuildContext context) async {
  final cardNumberController = TextEditingController();
  final expiryMonthController = TextEditingController();
  final expiryYearController = TextEditingController();
  final cvvController = TextEditingController();
  final mobileController = TextEditingController();
  final formKey = GlobalKey<FormState>();

  try {
    return await showModalBottomSheet<Map<String, String>>(
      context: context,
      isScrollControlled: true,
      backgroundColor: Colors.white,
      shape: const RoundedRectangleBorder(
        borderRadius: BorderRadius.vertical(top: Radius.circular(20)),
      ),
      builder: (ctx) {
        return Padding(
          padding: EdgeInsets.only(
            left: 20,
            right: 20,
            top: 20,
            bottom: MediaQuery.of(ctx).viewInsets.bottom + 20,
          ),
          child: StatefulBuilder(
            builder: (context, setModalState) {
              return Form(
                key: formKey,
                child: SingleChildScrollView(
                  child: Column(
                    mainAxisSize: MainAxisSize.min,
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Row(
                        mainAxisAlignment: MainAxisAlignment.spaceBetween,
                        children: [
                          const Text(
                            'PayFast Card Details',
                            style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold),
                          ),
                          IconButton(
                            icon: const Icon(Icons.close),
                            onPressed: () => Navigator.pop(ctx, null),
                          ),
                        ],
                      ),
                      const SizedBox(height: 12),
                      TextFormField(
                        controller: cardNumberController,
                        decoration: const InputDecoration(
                          labelText: 'Card Number',
                          hintText: '4123 4567 8901 2345',
                          prefixIcon: Icon(Icons.credit_card),
                          border: OutlineInputBorder(),
                        ),
                        keyboardType: TextInputType.number,
                        inputFormatters: [
                          FilteringTextInputFormatter.digitsOnly,
                          LengthLimitingTextInputFormatter(19),
                          PayFastCardNumberFormatter(),
                        ],
                        autofillHints: const <String>[],
                        validator: (v) {
                          if (v == null || v.replaceAll(' ', '').length < 13) {
                            return 'Enter a valid card number';
                          }
                          return null;
                        },
                      ),
                      const SizedBox(height: 12),
                      Row(
                        children: [
                          Expanded(
                            child: TextFormField(
                              controller: expiryMonthController,
                              decoration: const InputDecoration(
                                labelText: 'MM',
                                hintText: '08',
                                border: OutlineInputBorder(),
                              ),
                              keyboardType: TextInputType.number,
                              maxLength: 2,
                              validator: (v) {
                                final m = int.tryParse(v ?? '');
                                if (m == null || m < 1 || m > 12) return '01-12';
                                return null;
                              },
                            ),
                          ),
                          const SizedBox(width: 8),
                          Expanded(
                            child: TextFormField(
                              controller: expiryYearController,
                              decoration: const InputDecoration(
                                labelText: 'YYYY',
                                hintText: '2028',
                                border: OutlineInputBorder(),
                              ),
                              keyboardType: TextInputType.number,
                              maxLength: 4,
                              validator: (v) {
                                final y = int.tryParse(v ?? '');
                                if (y == null || y < DateTime.now().year) return 'Valid yr';
                                return null;
                              },
                            ),
                          ),
                          const SizedBox(width: 8),
                          Expanded(
                            child: TextFormField(
                              controller: cvvController,
                              decoration: const InputDecoration(
                                labelText: 'CVV',
                                hintText: '123',
                                border: OutlineInputBorder(),
                              ),
                              keyboardType: TextInputType.number,
                              obscureText: true,
                              maxLength: 4,
                              // PCI: block everything except digits, never
                              // allow autofill hints or paste of extra data.
                              inputFormatters: [
                                FilteringTextInputFormatter.digitsOnly,
                                LengthLimitingTextInputFormatter(4),
                              ],
                              autofillHints: const <String>[],
                              validator: (v) {
                                if (v == null || v.length < 3) return '3-4 digits';
                                return null;
                              },
                            ),
                          ),
                        ],
                      ),
                      const SizedBox(height: 8),
                      TextFormField(
                        controller: mobileController,
                        decoration: const InputDecoration(
                          labelText: 'Account / Mobile No.',
                          hintText: '03001234567',
                          prefixIcon: Icon(Icons.phone),
                          border: OutlineInputBorder(),
                        ),
                        keyboardType: TextInputType.phone,
                        validator: (v) {
                          if (v == null || v.isEmpty) return 'Enter mobile number';
                          return null;
                        },
                      ),
                      const SizedBox(height: 16),
                      SizedBox(
                        width: double.infinity,
                        height: 48,
                        child: ElevatedButton(
                          style: ElevatedButton.styleFrom(
                            backgroundColor: Colors.deepPurple,
                            foregroundColor: Colors.white,
                            shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(10)),
                          ),
                          onPressed: () {
                            if (formKey.currentState?.validate() ?? false) {
                              Navigator.pop(ctx, {
                                'card_number': cardNumberController.text.replaceAll(' ', '').trim(),
                                'expiry_month': expiryMonthController.text.trim(),
                                'expiry_year': expiryYearController.text.trim(),
                                'cvv': cvvController.text.trim(),
                                'customer_mobile_no': mobileController.text.trim(),
                              });
                            }
                          },
                          child: const Text('Authorize & Pay', style: TextStyle(fontWeight: FontWeight.bold)),
                        ),
                      ),
                    ],
                  ),
                ),
              );
            },
          ),
        );
      },
    );
  } finally {
    cardNumberController.dispose();
    expiryMonthController.dispose();
    expiryYearController.dispose();
    cvvController.dispose();
    mobileController.dispose();
  }
}

/// Renders the bank 3-D Secure challenge HTML inside an in-app WebView.
///
/// The ACS posts the PaRes result directly to the backend's signed
/// `data_3ds_callback_url`; this dialog only gates the user until they have
/// completed the challenge, mirroring standard 3DS app UX.
///
/// Returns true when the user indicates they completed verification.
Future<bool> showPayFast3DSChallenge(BuildContext context, String htmlContent) async {
  final controller = WebViewController();
  await controller.setJavaScriptMode(JavaScriptMode.unrestricted);

  // GAP-5 FIX: the post-3DS page (our 3ds_callback response) posts a message
  // to FlutterChannel — register the channel so the dialog auto-closes the
  // moment the bank OTP verification succeeds instead of waiting for the
  // user to tap the manual button.
  await controller.addJavaScriptChannel(
    'FlutterChannel',
    onMessageReceived: (msg) {
      if (msg.message.toLowerCase().contains('success') ||
          msg.message.toLowerCase().contains('verified')) {
        if (context.mounted) Navigator.of(context, rootNavigator: true).pop(true);
      }
    },
  );

  await controller.loadHtmlString(htmlContent);

  // The WebView load above is async — bail out if the screen went away mid-load.
  if (!context.mounted) return false;

  final result = await showDialog<bool>(
    context: context,
    barrierDismissible: false,
    builder: (dialogCtx) {
      return Dialog(
        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
        child: SizedBox(
          height: 520,
          width: double.maxFinite,
          child: Column(
            children: [
              AppBar(
                title: const Text('Bank 3D-Secure Verification', style: TextStyle(fontSize: 16, fontWeight: FontWeight.bold)),
                automaticallyImplyLeading: false,
                actions: [
                  IconButton(
                    icon: const Icon(Icons.close),
                    onPressed: () => Navigator.pop(dialogCtx, false),
                  ),
                ],
              ),
              Expanded(
                child: WebViewWidget(controller: controller),
              ),
              Padding(
                padding: const EdgeInsets.all(8.0),
                child: OutlinedButton(
                  onPressed: () => Navigator.pop(dialogCtx, true),
                  child: const Text('I Have Completed Verification'),
                ),
              ),
            ],
          ),
        ),
      );
    },
  );

  return result ?? false;
}

/// Formats PAN input as "4111 1111 1111 1111" while typing (digits only).
class PayFastCardNumberFormatter extends TextInputFormatter {
  @override
  TextEditingValue formatEditUpdate(
    TextEditingValue oldValue,
    TextEditingValue newValue,
  ) {
    final digits = newValue.text.replaceAll(' ', '');
    if (digits.isEmpty) return newValue;
    final buf = StringBuffer();
    for (var i = 0; i < digits.length; i++) {
      buf.write(digits[i]);
      if ((i + 1) % 4 == 0 && i + 1 != digits.length) buf.write(' ');
    }
    final text = buf.toString();
    return TextEditingValue(
      text: text,
      selection: TextSelection.collapsed(offset: text.length),
    );
  }
}
