import 'dart:io';
import 'package:flutter/material.dart';
import 'package:path_provider/path_provider.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/network/api_client.dart';
import '../../../../core/di/service_locator.dart';

class AdminExportScreen extends StatefulWidget {
  const AdminExportScreen({super.key});

  @override
  State<AdminExportScreen> createState() => _AdminExportScreenState();
}

class _AdminExportScreenState extends State<AdminExportScreen> {
  bool _isExporting = false;
  String? _lastExportPath;

  Future<void> _export1ibft() async {
    setState(() => _isExporting = true);
    try {
      final response = await sl<ApiClient>().get(ApiEndpoints.adminExport1ibft());
      if (response is Map<String, dynamic> && mounted) {
        final csv = response['csv']?.toString() ?? '';
        if (csv.isNotEmpty) {
          final directory = await getApplicationDocumentsDirectory();
          final timestamp = DateTime.now().millisecondsSinceEpoch;
          final file = File('${directory.path}/1ibft_export_$timestamp.csv');
          await file.writeAsString(csv);
          setState(() {
            _lastExportPath = file.path;
            _isExporting = false;
          });
          if (mounted) {
            ScaffoldMessenger.of(context).showSnackBar(
              SnackBar(
                content: Text('Exported to ${file.path.split('/').last}'),
                backgroundColor: Colors.green,
              ),
            );
          }
        }
      }
    } catch (e) {
      if (mounted) {
        setState(() => _isExporting = false);
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Export failed: $e'), backgroundColor: Colors.red),
        );
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: Colors.grey[100],
      appBar: AppBar(
        title: const Text('Export Data', style: TextStyle(fontWeight: FontWeight.bold, color: Colors.white)),
        backgroundColor: Colors.black87,
      ),
      body: SingleChildScrollView(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            _buildExportCard(
              title: '1LINK/1IBFT CSV Export',
              subtitle: 'Export vendor payouts for bank transfer via 1LINK/1IBFT system',
              icon: Icons.account_balance,
              color: Colors.blue,
              onExport: _export1ibft,
              isExporting: _isExporting,
            ),
            if (_lastExportPath != null) ...[
              const SizedBox(height: 16),
              Card(
                shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
                child: Padding(
                  padding: const EdgeInsets.all(16),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      const Text('Last Export', style: TextStyle(fontWeight: FontWeight.bold)),
                      const SizedBox(height: 8),
                      Text(_lastExportPath!.split('/').last, style: TextStyle(color: Colors.grey.shade600)),
                      const SizedBox(height: 12),
                      SizedBox(
                        width: double.infinity,
                        child: OutlinedButton.icon(
                          onPressed: () {
                            ScaffoldMessenger.of(context).showSnackBar(
                              SnackBar(content: Text('File saved: ${_lastExportPath!.split('/').last}')),
                            );
                          },
                          icon: const Icon(Icons.folder_open, size: 16),
                          label: const Text('View File'),
                          style: OutlinedButton.styleFrom(foregroundColor: Colors.blue),
                        ),
                      ),
                    ],
                  ),
                ),
              ),
            ],
          ],
        ),
      ),
    );
  }

  Widget _buildExportCard({
    required String title,
    required String subtitle,
    required IconData icon,
    required Color color,
    required VoidCallback onExport,
    required bool isExporting,
  }) {
    return Card(
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Container(
                  padding: const EdgeInsets.all(12),
                  decoration: BoxDecoration(
                    color: color.withOpacity(0.1),
                    borderRadius: BorderRadius.circular(8),
                  ),
                  child: Icon(icon, color: color, size: 28),
                ),
                const SizedBox(width: 12),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(title, style: const TextStyle(fontSize: 16, fontWeight: FontWeight.bold)),
                      const SizedBox(height: 4),
                      Text(subtitle, style: TextStyle(color: Colors.grey.shade600, fontSize: 12)),
                    ],
                  ),
                ),
              ],
            ),
            const SizedBox(height: 16),
            SizedBox(
              width: double.infinity,
              child: ElevatedButton.icon(
                onPressed: isExporting ? null : onExport,
                icon: isExporting
                    ? const SizedBox(width: 16, height: 16, child: CircularProgressIndicator(strokeWidth: 2, color: Colors.white))
                    : const Icon(Icons.download),
                label: Text(isExporting ? 'Exporting...' : 'Export CSV'),
                style: ElevatedButton.styleFrom(
                  backgroundColor: Colors.black87,
                  foregroundColor: Colors.white,
                  padding: const EdgeInsets.symmetric(vertical: 14),
                  shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}
