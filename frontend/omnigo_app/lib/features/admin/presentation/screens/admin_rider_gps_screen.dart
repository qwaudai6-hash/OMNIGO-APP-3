import 'package:flutter/material.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/network/api_client.dart';
import '../../../../core/di/service_locator.dart';

class AdminRiderGpsScreen extends StatefulWidget {
  const AdminRiderGpsScreen({super.key});

  @override
  State<AdminRiderGpsScreen> createState() => _AdminRiderGpsScreenState();
}

class _AdminRiderGpsScreenState extends State<AdminRiderGpsScreen> {
  final _riderController = TextEditingController();
  List<dynamic> _trail = [];
  bool _isLoading = false;
  bool _hasSearched = false;
  int _selectedHours = 24;

  @override
  void dispose() {
    _riderController.dispose();
    super.dispose();
  }

  Future<void> _fetchGpsTrail() async {
    final riderId = _riderController.text.trim();
    if (riderId.isEmpty) return;

    setState(() => _isLoading = true);
    try {
      final data = await sl<ApiClient>().get(
        ApiEndpoints.adminRiderGpsTrail(riderId, hours: _selectedHours),
      );
      if (data is Map<String, dynamic> && mounted) {
        setState(() {
          _trail = data['trail'] as List<dynamic>? ?? [];
          _isLoading = false;
          _hasSearched = true;
        });
      }
    } catch (e) {
      if (mounted) setState(() { _isLoading = false; _hasSearched = true; });
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: Colors.grey[100],
      appBar: AppBar(
        title: const Text('Rider GPS Trail', style: TextStyle(fontWeight: FontWeight.bold, color: Colors.white)),
        backgroundColor: Colors.black87,
      ),
      body: Column(
        children: [
          _buildSearchBar(),
          _buildTimeRangeSelector(),
          Expanded(
            child: _isLoading
                ? const Center(child: CircularProgressIndicator(color: Colors.black87))
                : !_hasSearched
                    ? const Center(child: Text('Enter Rider ID and tap Search'))
                    : _trail.isEmpty
                        ? const Center(child: Text('No GPS trail found'))
                        : _buildTrailList(),
          ),
        ],
      ),
    );
  }

  Widget _buildSearchBar() {
    return Container(
      padding: const EdgeInsets.all(12),
      color: Colors.white,
      child: Row(
        children: [
          Expanded(
            child: TextField(
              controller: _riderController,
              decoration: InputDecoration(
                hintText: 'Enter Rider ID (e.g., RIDR-4b5961b5)',
                hintStyle: TextStyle(color: Colors.grey.shade400),
                prefixIcon: const Icon(Icons.delivery_dining, color: Colors.grey),
                filled: true,
                fillColor: Colors.grey[100],
                border: OutlineInputBorder(
                  borderRadius: BorderRadius.circular(12),
                  borderSide: BorderSide.none,
                ),
              ),
              onSubmitted: (_) => _fetchGpsTrail(),
            ),
          ),
          const SizedBox(width: 8),
          ElevatedButton(
            onPressed: _fetchGpsTrail,
            style: ElevatedButton.styleFrom(
              backgroundColor: Colors.black87,
              foregroundColor: Colors.white,
              shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
              padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 14),
            ),
            child: const Text('Search'),
          ),
        ],
      ),
    );
  }

  Widget _buildTimeRangeSelector() {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
      color: Colors.white,
      child: Row(
        children: [
          Text('Time Range: ', style: TextStyle(color: Colors.grey.shade700, fontSize: 13)),
          ...[6, 12, 24, 48, 72].map((hours) => Padding(
            padding: const EdgeInsets.only(right: 8),
            child: ChoiceChip(
              label: Text('${hours}h', style: TextStyle(fontSize: 12, color: _selectedHours == hours ? Colors.white : Colors.grey)),
              selected: _selectedHours == hours,
              selectedColor: Colors.black87,
              onSelected: (_) => setState(() => _selectedHours = hours),
            ),
          )),
          const Spacer(),
          Text('${_trail.length} points', style: TextStyle(color: Colors.grey.shade600, fontSize: 12)),
        ],
      ),
    );
  }

  Widget _buildTrailList() {
    return ListView.builder(
      padding: const EdgeInsets.all(12),
      itemCount: _trail.length,
      itemBuilder: (context, index) {
        final point = _trail[index] as Map<String, dynamic>;
        final lat = (point['latitude'] as num?)?.toDouble() ?? 0;
        final lng = (point['longitude'] as num?)?.toDouble() ?? 0;
        final speed = (point['speed'] as num?)?.toDouble() ?? 0;
        final battery = (point['battery_pct'] as num?)?.toInt() ?? 0;
        final timestamp = point['timestamp']?.toString() ?? '';
        final bearing = (point['bearing'] as num?)?.toInt() ?? 0;

        final isFirst = index == 0;
        final isLast = index == _trail.length - 1;

        return Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Column(
              children: [
                Icon(
                  isFirst ? Icons.location_on : isLast ? Icons.flag : Icons.circle,
                  color: isFirst ? Colors.green : isLast ? Colors.red : Colors.blue,
                  size: isFirst || isLast ? 24 : 12,
                ),
                if (!isLast)
                  Container(width: 2, height: 40, color: Colors.blue.shade200),
              ],
            ),
            const SizedBox(width: 12),
            Expanded(
              child: Card(
                margin: const EdgeInsets.only(bottom: 8),
                shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(8)),
                child: Padding(
                  padding: const EdgeInsets.all(12),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Row(
                        mainAxisAlignment: MainAxisAlignment.spaceBetween,
                        children: [
                          Text(
                            isFirst ? 'START' : isLast ? 'END' : 'Point ${index + 1}',
                            style: TextStyle(
                              fontWeight: FontWeight.bold,
                              fontSize: 12,
                              color: isFirst ? Colors.green : isLast ? Colors.red : Colors.blue,
                            ),
                          ),
                          Text(timestamp, style: TextStyle(fontSize: 11, color: Colors.grey.shade600)),
                        ],
                      ),
                      const SizedBox(height: 4),
                      Text('Lat: ${lat.toStringAsFixed(4)}, Lng: ${lng.toStringAsFixed(4)}', style: const TextStyle(fontSize: 12)),
                      const SizedBox(height: 4),
                      Row(
                        children: [
                          _infoChip(Icons.speed, '${speed.toStringAsFixed(1)} km/h'),
                          const SizedBox(width: 8),
                          _infoChip(Icons.battery_std, '$battery%'),
                          const SizedBox(width: 8),
                          _infoChip(Icons.navigation, '$bearing°'),
                        ],
                      ),
                    ],
                  ),
                ),
              ),
            ),
          ],
        );
      },
    );
  }

  Widget _infoChip(IconData icon, String text) {
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        Icon(icon, size: 14, color: Colors.grey.shade600),
        const SizedBox(width: 2),
        Text(text, style: TextStyle(fontSize: 11, color: Colors.grey.shade600)),
      ],
    );
  }
}
