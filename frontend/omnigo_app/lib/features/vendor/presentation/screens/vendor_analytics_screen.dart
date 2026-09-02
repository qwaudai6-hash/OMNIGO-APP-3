import 'dart:async';
import 'package:flutter/material.dart';
import '../../../../core/theme/app_theme.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/di/service_locator.dart';
import '../../../../core/network/api_client.dart';

class VendorAnalyticsScreen extends StatefulWidget {

  const VendorAnalyticsScreen({super.key, required this.vendorTrackingId});
  final String vendorTrackingId;

  @override
  VendorAnalyticsScreenState createState() => VendorAnalyticsScreenState();
}

class VendorAnalyticsScreenState extends State<VendorAnalyticsScreen> with SingleTickerProviderStateMixin {
  bool _isLoading = true;
  bool _isLoadingOrders = true;
  String? _errorMessage;
  Map<String, dynamic>? _metricsData;
  List<dynamic> _orders = [];
  late AnimationController _animationController;
  late Animation<double> _fadeAnimation;

  @override
  void initState() {
    super.initState();
    _animationController = AnimationController(vsync: this, duration: const Duration(milliseconds: 800));
    _fadeAnimation = CurvedAnimation(parent: _animationController, curve: Curves.easeOut);
    _fetchMetrics();
  }

  @override
  void dispose() {
    _animationController.dispose();
    super.dispose();
  }

  Future<void> _fetchOrders() async {
    try {
      final api = sl<ApiClient>();
      final response = await api.get(ApiEndpoints.vendorOrders(widget.vendorTrackingId));

      if (response is List<dynamic>) {
        if (mounted) {
          setState(() {
            _orders = response;
            _isLoadingOrders = false;
          });
        }
      } else {
        if (mounted) {
          setState(() {
            _isLoadingOrders = false;
          });
        }
      }
    } catch (e) {
      debugPrint('Failed to load orders for vendor analytics: $e');
      if (mounted) {
        setState(() {
          _isLoadingOrders = false;
        });
      }
    }
  }

  Future<void> _fetchMetrics() async {
    if (!mounted) return;
    setState(() {
      _isLoading = true;
      _isLoadingOrders = true;
      _errorMessage = null;
    });

    await _fetchOrders();

    try {
      final api = sl<ApiClient>();
      final response = await api.get(ApiEndpoints.vendorMetrics(widget.vendorTrackingId));

      // #39: Handle both wrapped {data: [...]} and plain response formats
      Map<String, dynamic>? metrics;
      if (response is Map<String, dynamic>) {
        metrics = response['data'] is Map<String, dynamic> ? response['data'] as Map<String, dynamic> : response;
      } else if (response is List<dynamic> && response.isNotEmpty) {
        metrics = response.first as Map<String, dynamic>;
      }

      if (metrics != null) {
        if (!mounted) return;
        setState(() {
          _metricsData = metrics;
          _isLoading = false;
        });
        unawaited(_animationController.forward());
      } else {
        if (!mounted) return;
        setState(() {
          _errorMessage = 'Failed to load telemetry.';
          _isLoading = false;
        });
      }
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _errorMessage = 'Connection Error: ${e.toString()}';
        _isLoading = false;
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: AppTheme.blackAccent, // Deep Space Dark Mode
      appBar: AppBar(
        backgroundColor: Colors.transparent,
        elevation: 0,
        title: const Text('Store Telemetry', style: TextStyle(color: Colors.white, fontWeight: FontWeight.bold, fontSize: 20)),
        leading: IconButton(
          icon: const Icon(Icons.arrow_back_rounded, color: Colors.white),
          onPressed: () => Navigator.pop(context),
        ),
        actions: [
          IconButton(
            icon: const Icon(Icons.sync_rounded, color: AppTheme.limeAccent),
            onPressed: _fetchMetrics,
          ),
        ],
      ),
      body: _buildBody(),
    );
  }

  Widget _buildBody() {
    if (_isLoading) {
      return const Center(
        child: CircularProgressIndicator(color: AppTheme.limeAccent),
      );
    }

    if (_errorMessage != null) {
      return Center(
        child: Column(
          mainAxisAlignment: MainAxisAlignment.center,
          children: [
            const Icon(Icons.error_outline_rounded, color: Colors.redAccent, size: 64),
            const SizedBox(height: 16),
            Text(_errorMessage!, style: const TextStyle(color: Colors.grey), textAlign: TextAlign.center),
            const SizedBox(height: 24),
            ElevatedButton(
              onPressed: _fetchMetrics,
              style: ElevatedButton.styleFrom(backgroundColor: AppTheme.limeAccent, foregroundColor: Colors.black),
              child: const Text('Retry Telemetry'),
            ),
          ],
        ),
      );
    }

    if (_metricsData == null) {
      return const SizedBox.shrink();
    }

    final double totalRevenue = (_metricsData!['total_revenue'] as num?)?.toDouble() ?? 0.0;
    final double wowGrowth = (_metricsData!['wow_growth_percentage'] as num?)?.toDouble() ?? 0.0;
    final int completedOrders = (_metricsData!['completed_orders'] as num?)?.toInt() ?? 0;
    final int pendingOrders = (_metricsData!['pending_orders'] as num?)?.toInt() ?? 0;
    final int activeProducts = (_metricsData!['active_products'] as num?)?.toInt() ?? 0;
    final double currentWeek = (_metricsData!['current_week_revenue'] as num?)?.toDouble() ?? 0.0;
    final List<dynamic> trends = (_metricsData!['daily_trends'] as List<dynamic>?) ?? [];

    return FadeTransition(
      opacity: _fadeAnimation,
      child: RefreshIndicator(
        color: AppTheme.limeAccent,
        backgroundColor: Colors.black,
        onRefresh: _fetchMetrics,
        child: ListView(
          physics: const BouncingScrollPhysics(),
          padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 10),
          children: [
            _buildPrimaryKpi(totalRevenue, wowGrowth),
            const SizedBox(height: 24),
            _buildGridMetrics(completedOrders, pendingOrders, activeProducts, currentWeek),
            const SizedBox(height: 32),
            _buildSparklineChart(trends),
            const SizedBox(height: 32),
            _buildOrderHistory(),
            const SizedBox(height: 40),
          ],
        ),
      ),
    );
  }

  Widget _buildPrimaryKpi(double revenue, double growth) {
    final bool isPositive = growth >= 0;
    return Container(
      padding: const EdgeInsets.all(28),
      decoration: BoxDecoration(
        color: const Color(0xFF16161D),
        borderRadius: BorderRadius.circular(32),
        border: Border.all(color: Colors.white.withOpacity(0.05)),
        boxShadow: [
          BoxShadow(
            color: AppTheme.limeAccent.withOpacity(0.05),
            blurRadius: 40,
            offset: const Offset(0, 10),
          ),
        ],
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Container(
                padding: const EdgeInsets.all(8),
                decoration: BoxDecoration(
                  color: AppTheme.limeAccent.withOpacity(0.1),
                  borderRadius: BorderRadius.circular(12),
                ),
                child: const Icon(Icons.account_balance_wallet_rounded, color: AppTheme.limeAccent, size: 24),
              ),
              const SizedBox(width: 12),
              const Text(
                'Total Revenue',
                style: TextStyle(color: Colors.grey, fontSize: 16, fontWeight: FontWeight.w500),
              ),
            ],
          ),
          const SizedBox(height: 24),
          Text(
            'PKR ${revenue.toStringAsFixed(2)}',
            style: const TextStyle(color: Colors.white, fontSize: 42, fontWeight: FontWeight.w900, letterSpacing: -1),
          ),
          const SizedBox(height: 16),
          Row(
            children: [
              Container(
                padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 6),
                decoration: BoxDecoration(
                  color: isPositive ? Colors.green.withOpacity(0.1) : Colors.red.withOpacity(0.1),
                  borderRadius: BorderRadius.circular(12),
                ),
                child: Row(
                  children: [
                    Icon(
                      isPositive ? Icons.trending_up_rounded : Icons.trending_down_rounded,
                      color: isPositive ? Colors.greenAccent : Colors.redAccent,
                      size: 16,
                    ),
                    const SizedBox(width: 6),
                    Text(
                      '${growth.abs().toStringAsFixed(1)}% WoW',
                      style: TextStyle(
                        color: isPositive ? Colors.greenAccent : Colors.redAccent,
                        fontWeight: FontWeight.bold,
                        fontSize: 13,
                      ),
                    ),
                  ],
                ),
              ),
              const SizedBox(width: 12),
              const Text('vs previous week', style: TextStyle(color: Colors.grey, fontSize: 12)),
            ],
          ),
        ],
      ),
    );
  }

  Widget _buildGridMetrics(int completed, int pending, int activeProducts, double currentWeek) {
    return GridView.count(
      crossAxisCount: 2,
      shrinkWrap: true,
      physics: const NeverScrollableScrollPhysics(),
      crossAxisSpacing: 16,
      mainAxisSpacing: 16,
      childAspectRatio: 1.1,
      children: [
        _buildMetricCard('Orders Done', completed.toString(), Icons.check_circle_outline_rounded, Colors.blueAccent),
        _buildMetricCard('Pending Ops', pending.toString(), Icons.hourglass_empty_rounded, Colors.orangeAccent),
        _buildMetricCard('Live Catalog', activeProducts.toString(), Icons.storefront_rounded, Colors.purpleAccent),
        _buildMetricCard('This Week', 'PKR ${currentWeek.toStringAsFixed(0)}', Icons.timeline_rounded, AppTheme.limeAccent),
      ],
    );
  }

  Widget _buildMetricCard(String title, String value, IconData icon, Color color) {
    return Container(
      padding: const EdgeInsets.all(20),
      decoration: BoxDecoration(
        color: const Color(0xFF16161D),
        borderRadius: BorderRadius.circular(24),
        border: Border.all(color: Colors.white.withOpacity(0.05)),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        mainAxisAlignment: MainAxisAlignment.center,
        children: [
          Icon(icon, color: color, size: 28),
          const Spacer(),
          Text(
            value,
            style: const TextStyle(color: Colors.white, fontSize: 24, fontWeight: FontWeight.bold),
          ),
          const SizedBox(height: 4),
          Text(
            title,
            style: const TextStyle(color: Colors.grey, fontSize: 13),
          ),
        ],
      ),
    );
  }

  Widget _buildSparklineChart(List<dynamic> trends) {
    if (trends.isEmpty) return const SizedBox.shrink();

    // Parse data points
    final List<double> data = trends.map((t) => (t['revenue'] as num).toDouble()).toList();
    final double currentVelocity = data.isNotEmpty ? data.last : 0.0;

    return Container(
      padding: const EdgeInsets.all(24),
      decoration: BoxDecoration(
        color: const Color(0xFF16161D),
        borderRadius: BorderRadius.circular(32),
        border: Border.all(color: Colors.white.withOpacity(0.05)),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            mainAxisAlignment: MainAxisAlignment.spaceBetween,
            children: [
              const Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    'Revenue Velocity',
                    style: TextStyle(color: Colors.white, fontSize: 16, fontWeight: FontWeight.bold),
                  ),
                  SizedBox(height: 4),
                  Text(
                    'Real-time sparkline custom paint graphics',
                    style: TextStyle(color: Colors.grey, fontSize: 12),
                  ),
                ],
              ),
              Container(
                padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 4),
                decoration: BoxDecoration(
                  color: AppTheme.limeAccent.withOpacity(0.1),
                  borderRadius: BorderRadius.circular(12),
                ),
                child: Text(
                  'PKR ${currentVelocity.toStringAsFixed(0)}',
                  style: const TextStyle(color: AppTheme.limeAccent, fontWeight: FontWeight.bold, fontSize: 12),
                ),
              ),
            ],
          ),
          const SizedBox(height: 32),
          // Custom Paint Sparkline Canvas
          SizedBox(
            height: 160,
            width: double.infinity,
            child: CustomPaint(
              painter: SparklinePainter(data, color: AppTheme.limeAccent),
            ),
          ),
          const SizedBox(height: 20),
          // X-Axis Date labels
          Row(
            mainAxisAlignment: MainAxisAlignment.spaceBetween,
            children: trends.map((t) {
              final String label = t['date'].toString().substring(5); // MM-DD
              return Text(
                label,
                style: const TextStyle(color: Colors.white70, fontSize: 11, fontWeight: FontWeight.w600),
              );
            }).toList(),
          ),
        ],
      ),
    );
  }

  Widget _buildOrderHistory() {
    if (_isLoadingOrders) {
      return const Center(
        child: CircularProgressIndicator(color: AppTheme.limeAccent),
      );
    }

    if (_orders.isEmpty) {
      return Container(
        padding: const EdgeInsets.all(24),
        decoration: BoxDecoration(
          color: const Color(0xFF16161D),
          borderRadius: BorderRadius.circular(24),
          border: Border.all(color: Colors.white.withOpacity(0.05)),
        ),
        child: const Center(
          child: Text('No orders processed yet.', style: TextStyle(color: Colors.grey)),
        ),
      );
    }

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        const Text(
          'Purchase & Delivery History',
          style: TextStyle(color: Colors.white, fontSize: 18, fontWeight: FontWeight.bold),
        ),
        const SizedBox(height: 16),
        ListView.separated(
          shrinkWrap: true,
          physics: const NeverScrollableScrollPhysics(),
          itemCount: _orders.length,
          separatorBuilder: (context, index) => const SizedBox(height: 12),
          itemBuilder: (context, index) {
            final order = _orders[index] as Map<String, dynamic>;
            final orderId = (order['order_tracking_id'] as String?) ?? 'ORD-UNKNOWN';
            final customerName = (order['customer_name'] as String?) ?? 'Unknown Customer';
            final customerPhone = (order['customer_phone'] as String?) ?? '';
            final riderName = (order['rider_name'] as String?) ?? 'Unassigned Rider';
            final riderPhone = (order['rider_phone'] as String?) ?? '';
            final status = (order['status'] as String?) ?? 'pending';
            final gateway = (order['payment_gateway'] as String?) ?? 'COD';
            final totalAmount = (order['total_amount'] ?? 0.0).toString();
            final dateStr = order['created_at'] != null 
                ? DateTime.parse(order['created_at'].toString()).toLocal().toString().substring(0, 16)
                : 'Recent';

            // Styling status color
            Color statusColor = Colors.orangeAccent;
            if (status == 'completed') {
              statusColor = AppTheme.limeAccent;
            } else if (status == 'failed' || status == 'cancelled') {
              statusColor = Colors.redAccent;
            } else if (status == 'accepted' || status == 'shipped') {
              statusColor = Colors.blueAccent;
            }

            return Container(
              padding: const EdgeInsets.all(20),
              decoration: BoxDecoration(
                color: const Color(0xFF16161D),
                borderRadius: BorderRadius.circular(24),
                border: Border.all(color: Colors.white.withOpacity(0.05)),
              ),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Row(
                    mainAxisAlignment: MainAxisAlignment.spaceBetween,
                    children: [
                      Text(
                        'ID: $orderId',
                        style: const TextStyle(color: Colors.white, fontWeight: FontWeight.bold, fontSize: 14),
                      ),
                      Row(
                        children: [
                          Container(
                            padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 4),
                            decoration: BoxDecoration(
                              color: Colors.white.withOpacity(0.08),
                              borderRadius: BorderRadius.circular(10),
                            ),
                            child: Text(
                              gateway.toString().toUpperCase(),
                              style: const TextStyle(color: Colors.white70, fontWeight: FontWeight.bold, fontSize: 9),
                            ),
                          ),
                          const SizedBox(width: 6),
                          Container(
                            padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 4),
                            decoration: BoxDecoration(
                              color: statusColor.withOpacity(0.1),
                              borderRadius: BorderRadius.circular(10),
                            ),
                            child: Text(
                              status.toUpperCase(),
                              style: TextStyle(color: statusColor, fontWeight: FontWeight.bold, fontSize: 9),
                            ),
                          ),
                        ],
                      ),
                    ],
                  ),
                  const SizedBox(height: 12),
                  const Divider(color: Colors.white12),
                  const SizedBox(height: 8),
                  Row(
                    children: [
                      const Icon(Icons.person_outline_rounded, color: Colors.grey, size: 18),
                      const SizedBox(width: 8),
                      Text(
                        'Customer: ',
                        style: TextStyle(color: Colors.grey[700], fontSize: 13),
                      ),
                      Text(
                        customerName,
                        style: const TextStyle(color: Colors.white, fontWeight: FontWeight.w600, fontSize: 13),
                      ),
                    ],
                  ),
                  if (customerPhone.isNotEmpty) ...[
                    const SizedBox(height: 6),
                    Row(
                      children: [
                        const Icon(Icons.phone_outlined, color: Colors.grey, size: 18),
                        const SizedBox(width: 8),
                        Text(
                          'Phone: $customerPhone',
                          style: const TextStyle(color: Colors.white70, fontSize: 12),
                        ),
                      ],
                    ),
                  ],
                  const SizedBox(height: 12),
                  Row(
                    children: [
                      const Icon(Icons.two_wheeler_rounded, color: Colors.grey, size: 18),
                      const SizedBox(width: 8),
                      Text(
                        'Rider: ',
                        style: TextStyle(color: Colors.grey[700], fontSize: 13),
                      ),
                      Text(
                        riderName,
                        style: TextStyle(
                          color: riderName == 'Unassigned Rider' ? Colors.redAccent : Colors.white,
                          fontWeight: FontWeight.w600,
                          fontSize: 13,
                        ),
                      ),
                    ],
                  ),
                  if (riderPhone.isNotEmpty) ...[
                    const SizedBox(height: 6),
                    Row(
                      children: [
                        const Icon(Icons.phone_enabled_outlined, color: Colors.grey, size: 18),
                        const SizedBox(width: 8),
                        Text(
                          'Rider Contact: $riderPhone',
                          style: const TextStyle(color: Colors.white70, fontSize: 12),
                        ),
                      ],
                    ),
                  ],
                  const SizedBox(height: 12),
                  const Divider(color: Colors.white12),
                  const SizedBox(height: 8),
                  Row(
                    mainAxisAlignment: MainAxisAlignment.spaceBetween,
                    children: [
                      Text(
                        dateStr,
                        style: const TextStyle(color: Colors.grey, fontSize: 12),
                      ),
                      Text(
                        'PKR $totalAmount',
                        style: const TextStyle(color: AppTheme.limeAccent, fontWeight: FontWeight.bold, fontSize: 16),
                      ),
                    ],
                  ),
                ],
              ),
            );
          },
        ),
      ],
    );
  }
}

class SparklinePainter extends CustomPainter {

  SparklinePainter(this.data, {this.color = AppTheme.limeAccent});
  final List<double> data;
  final Color color;

  @override
  void paint(Canvas canvas, Size size) {
    if (data.isEmpty) return;

    final paint = Paint()
      ..color = color
      ..style = PaintingStyle.stroke
      ..strokeWidth = 3.0
      ..strokeCap = StrokeCap.round;

    if (data.length <= 1) {
      canvas.drawCircle(Offset(size.width / 2, size.height / 2), 4, paint);
      return;
    }

    final fillPaint = Paint()
      ..shader = LinearGradient(
        begin: Alignment.topCenter,
        end: Alignment.bottomCenter,
        colors: [color.withOpacity(0.3), color.withOpacity(0.0)],
      ).createShader(Rect.fromLTWH(0, 0, size.width, size.height))
      ..style = PaintingStyle.fill;

    final double maxVal = data.reduce((a, b) => a > b ? a : b);
    final double minVal = data.reduce((a, b) => a < b ? a : b);
    final double range = (maxVal - minVal) == 0 ? 1 : (maxVal - minVal);

    final path = Path();
    final fillPath = Path();

    final double widthStep = size.width / (data.length - 1);

    for (int i = 0; i < data.length; i++) {
      final double x = i * widthStep;
      // Invert Y axis because in canvas, (0,0) is top-left
      final double normalizedVal = (data[i] - minVal) / range;
      final double y = size.height - (normalizedVal * size.height * 0.8 + size.height * 0.1);

      if (i == 0) {
        path.moveTo(x, y);
        fillPath.moveTo(x, size.height);
        fillPath.lineTo(x, y);
      } else {
        // Draw smooth bezier curves
        final double prevX = (i - 1) * widthStep;
        final double prevNormalizedVal = (data[i - 1] - minVal) / range;
        final double prevY = size.height - (prevNormalizedVal * size.height * 0.8 + size.height * 0.1);
        
        final double controlX1 = prevX + widthStep / 2;
        final double controlY1 = prevY;
        final double controlX2 = prevX + widthStep / 2;
        final double controlY2 = y;
        
        path.cubicTo(controlX1, controlY1, controlX2, controlY2, x, y);
        fillPath.cubicTo(controlX1, controlY1, controlX2, controlY2, x, y);
      }

      if (i == data.length - 1) {
        fillPath.lineTo(x, size.height);
        fillPath.close();
      }
    }

    canvas.drawPath(fillPath, fillPaint);
    canvas.drawPath(path, paint);
  }

  @override
  bool shouldRepaint(covariant SparklinePainter oldDelegate) {
    return oldDelegate.data != data;
  }
}
