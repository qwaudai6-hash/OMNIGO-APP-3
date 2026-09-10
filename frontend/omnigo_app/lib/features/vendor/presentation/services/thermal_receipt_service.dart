import 'package:flutter/material.dart';
import 'package:intl/intl.dart';
import 'package:pdf/pdf.dart';
import 'package:pdf/widgets.dart' as pw;
import 'package:printing/printing.dart';

/// ThermalReceiptService generates ESC/POS compatible 58mm/80mm receipt slips
/// for vendor food and retail deliveries using standard thermal printer formatting.
class ThermalReceiptService {
  ThermalReceiptService._();

  /// Prints or previews an order slip for thermal printers (58mm roll width).
  static Future<void> printSlip({
    required BuildContext context,
    required Map<String, dynamic> order,
    String? vendorTrackingId,
    bool roll80 = false,
  }) async {
    final orderId = (order['order_tracking_id'] as String?) ??
        (order['tracking_id'] as String?) ??
        'ORD-UNKNOWN';
    final storeId = (order['store_tracking_id'] as String?) ??
        (order['store_id'] as String?) ??
        'STOR-UNKNOWN';
    final customerId = (order['customer_tracking_id'] as String?) ?? 'CUST-UNKNOWN';
    final totalAmount = ((order['total_amount'] as num?) ?? 0.0).toDouble();
    final currency = (order['currency'] as String?) ?? 'PKR';
    final items = (order['items'] as List<dynamic>?) ?? <dynamic>[];
    final otpCode = (order['otp_code'] as String?) ?? '';
    final createdAtStr = order['created_at']?.toString();

    DateTime orderDate;
    if (createdAtStr != null) {
      orderDate = DateTime.tryParse(createdAtStr) ?? DateTime.now();
    } else {
      orderDate = DateTime.now();
    }
    final formattedDate = DateFormat('dd MMM yyyy, hh:mm a').format(orderDate.toLocal());

    final pageFormat = roll80 ? PdfPageFormat.roll80 : PdfPageFormat.roll57;

    try {
      await Printing.layoutPdf(
        name: 'OrderSlip_$orderId',
        format: pageFormat,
        onLayout: (PdfPageFormat format) async {
          final doc = pw.Document();

          doc.addPage(
            pw.Page(
              pageFormat: format,
              margin: const pw.EdgeInsets.symmetric(horizontal: 6, vertical: 8),
              build: (pw.Context docContext) {
                return pw.Column(
                  crossAxisAlignment: pw.CrossAxisAlignment.start,
                  children: [
                    // Header
                    pw.Center(
                      child: pw.Text(
                        'OMNIGO SUPER APP',
                        style: pw.TextStyle(
                          fontWeight: pw.FontWeight.bold,
                          fontSize: 13,
                        ),
                      ),
                    ),
                    pw.Center(
                      child: pw.Text(
                        'VENDOR DISPATCH SLIP',
                        style: pw.TextStyle(
                          fontWeight: pw.FontWeight.bold,
                          fontSize: 9,
                        ),
                      ),
                    ),
                    pw.SizedBox(height: 4),
                    pw.Divider(thickness: 0.8, borderStyle: pw.BorderStyle.dashed),

                    // Order & Store Metadata
                    pw.Row(
                      mainAxisAlignment: pw.MainAxisAlignment.spaceBetween,
                      children: [
                        pw.Text('ORDER #:', style: pw.TextStyle(fontSize: 8, fontWeight: pw.FontWeight.bold)),
                        pw.Text(orderId, style: pw.TextStyle(fontSize: 8, fontWeight: pw.FontWeight.bold)),
                      ],
                    ),
                    pw.SizedBox(height: 2),
                    pw.Row(
                      mainAxisAlignment: pw.MainAxisAlignment.spaceBetween,
                      children: [
                        pw.Text('STORE ID:', style: const pw.TextStyle(fontSize: 8)),
                        pw.Text(storeId, style: const pw.TextStyle(fontSize: 8)),
                      ],
                    ),
                    if (vendorTrackingId != null) ...[
                      pw.SizedBox(height: 2),
                      pw.Row(
                        mainAxisAlignment: pw.MainAxisAlignment.spaceBetween,
                        children: [
                          pw.Text('VENDOR:', style: const pw.TextStyle(fontSize: 8)),
                          pw.Text(vendorTrackingId, style: const pw.TextStyle(fontSize: 8)),
                        ],
                      ),
                    ],
                    pw.SizedBox(height: 2),
                    pw.Row(
                      mainAxisAlignment: pw.MainAxisAlignment.spaceBetween,
                      children: [
                        pw.Text('CUSTOMER:', style: const pw.TextStyle(fontSize: 8)),
                        pw.Text(customerId, style: const pw.TextStyle(fontSize: 8)),
                      ],
                    ),
                    pw.SizedBox(height: 2),
                    pw.Row(
                      mainAxisAlignment: pw.MainAxisAlignment.spaceBetween,
                      children: [
                        pw.Text('DATE:', style: const pw.TextStyle(fontSize: 8)),
                        pw.Text(formattedDate, style: const pw.TextStyle(fontSize: 8)),
                      ],
                    ),

                    pw.SizedBox(height: 4),
                    pw.Divider(thickness: 0.8, borderStyle: pw.BorderStyle.dashed),

                    // Barcode
                    pw.Center(
                      child: pw.BarcodeWidget(
                        barcode: pw.Barcode.code128(),
                        data: orderId,
                        width: 140,
                        height: 36,
                        drawText: true,
                        textStyle: const pw.TextStyle(fontSize: 7),
                      ),
                    ),

                    pw.SizedBox(height: 4),
                    pw.Divider(thickness: 0.8, borderStyle: pw.BorderStyle.dashed),

                    // Item breakdown if available
                    if (items.isNotEmpty) ...[
                      pw.Text('ORDER ITEMS:', style: pw.TextStyle(fontSize: 8, fontWeight: pw.FontWeight.bold)),
                      pw.SizedBox(height: 2),
                      ...items.map((it) {
                        final item = it as Map<String, dynamic>;
                        final name = (item['title'] ?? item['name'] ?? 'Item').toString();
                        final qty = item['quantity'] ?? 1;
                        final price = ((item['price'] as num?) ?? 0.0).toDouble();
                        return pw.Padding(
                          padding: const pw.EdgeInsets.symmetric(vertical: 1),
                          child: pw.Row(
                            mainAxisAlignment: pw.MainAxisAlignment.spaceBetween,
                            children: [
                              pw.Expanded(
                                child: pw.Text(
                                  '$qty x $name',
                                  style: const pw.TextStyle(fontSize: 8),
                                  maxLines: 1,
                                ),
                              ),
                              pw.Text(
                                '$currency ${(price * (qty is int ? qty : 1)).toStringAsFixed(2)}',
                                style: const pw.TextStyle(fontSize: 8),
                              ),
                            ],
                          ),
                        );
                      }),
                      pw.SizedBox(height: 4),
                      pw.Divider(thickness: 0.8, borderStyle: pw.BorderStyle.dashed),
                    ],

                    // Total
                    pw.Row(
                      mainAxisAlignment: pw.MainAxisAlignment.spaceBetween,
                      children: [
                        pw.Text(
                          'TOTAL AMOUNT:',
                          style: pw.TextStyle(fontSize: 10, fontWeight: pw.FontWeight.bold),
                        ),
                        pw.Text(
                          '$currency ${totalAmount.toStringAsFixed(2)}',
                          style: pw.TextStyle(fontSize: 10, fontWeight: pw.FontWeight.bold),
                        ),
                      ],
                    ),

                    // Delivery Handover / OTP box
                    pw.SizedBox(height: 6),
                    pw.Center(
                      child: pw.Container(
                        padding: const pw.EdgeInsets.symmetric(horizontal: 10, vertical: 4),
                        decoration: pw.BoxDecoration(
                          border: pw.Border.all(width: 1),
                          borderRadius: const pw.BorderRadius.all(pw.Radius.circular(4)),
                        ),
                        child: pw.Column(
                          children: [
                            pw.Text(
                              'READY FOR RIDER PICKUP',
                              style: pw.TextStyle(fontSize: 8, fontWeight: pw.FontWeight.bold),
                            ),
                            if (otpCode.isNotEmpty) ...[
                              pw.SizedBox(height: 2),
                              pw.Text(
                                'HANDOVER OTP: $otpCode',
                                style: pw.TextStyle(fontSize: 9, fontWeight: pw.FontWeight.bold),
                              ),
                            ],
                          ],
                        ),
                      ),
                    ),
                    pw.SizedBox(height: 6),
                    pw.Center(
                      child: pw.Text(
                        'Thank you for partnering with OmniGo!',
                        style: const pw.TextStyle(fontSize: 7),
                      ),
                    ),
                  ],
                );
              },
            ),
          );

          return doc.save();
        },
      );
    } catch (e) {
      debugPrint('[ThermalReceiptService] Error printing receipt: $e');
      if (context.mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(
            content: Text('Failed to generate thermal receipt: $e'),
            backgroundColor: Colors.red,
          ),
        );
      }
    }
  }
}
