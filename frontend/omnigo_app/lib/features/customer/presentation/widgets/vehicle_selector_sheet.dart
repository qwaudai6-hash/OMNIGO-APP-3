import 'dart:async';
import 'dart:convert';
import 'dart:ui';
import 'package:flutter/material.dart';
import '../../../../core/di/service_locator.dart';
import '../../../../core/network/api_client.dart';
import '../../../../core/network/websocket_client.dart';
import '../../../../core/services/session_registry.dart';

class VehicleSelectorSheet extends StatefulWidget {

  const VehicleSelectorSheet({
    super.key,
    required this.estimates,
    required this.onBookRide,
  });
  final List<dynamic> estimates;
  final void Function(String vehicleType, double fare) onBookRide;

  @override
  State<VehicleSelectorSheet> createState() => _VehicleSelectorSheetState();
}

class _VehicleSelectorSheetState extends State<VehicleSelectorSheet> with SingleTickerProviderStateMixin {
  String _selectedVehicle = '';
  double _selectedFare = 0.0;
  String _serviceType = 'passenger'; // 'passenger' or 'courier'
  bool _customOfferEnabled = false;
  double _negotiatedFare = 0.0;
  String _biddingStatus = 'idle'; // 'idle' | 'searching' | 'offers_received'
  List<Map<String, dynamic>> _riderOffers = [];
  final TextEditingController _customFareController = TextEditingController();

  late AnimationController _animationController;
  late Animation<double> _slideAnimation;
  StreamSubscription<dynamic>? _wsSubscription;

  @override
  void initState() {
    super.initState();
    if (widget.estimates.isNotEmpty) {
      _selectedVehicle = (widget.estimates.first['vehicle_type'] as String?) ?? '';
      _selectedFare = (widget.estimates.first['total_fare'] as num?)?.toDouble() ?? 0.0;
      _negotiatedFare = _selectedFare;
      _customFareController.text = _selectedFare.toStringAsFixed(0);
    }

    _animationController = AnimationController(
      vsync: this,
      duration: const Duration(milliseconds: 400),
    );

    _slideAnimation = Tween<double>(begin: 1.0, end: 0.0).animate(
      CurvedAnimation(parent: _animationController, curve: Curves.easeOutBack),
    );

    _animationController.forward();
    _listenToRealtimeBids();
  }

  void _listenToRealtimeBids() {
    final wsClient = sl<WebSocketClient>();
    _wsSubscription = wsClient.stream.listen((message) {
      if (message is! String) return;
      try {
        final data = jsonDecode(message) as Map<String, dynamic>;
        if (data['action'] == 'RIDER_COUNTER_OFFER' && mounted) {
          final newOffer = {
            'rider_name': (data['rider_name'] as String?) ?? 'Rider',
            'rating': (data['rating'] as String?) ?? '4.8 ★',
            'plate': (data['plate'] as String?) ?? 'LHR-LIVE',
            'fare': (data['proposed_fare'] as num?)?.toDouble() ?? _negotiatedFare,
            'eta': (data['eta'] as String?) ?? '3 min',
          };
          setState(() {
            _riderOffers.add(newOffer);
            _biddingStatus = 'offers_received';
          });
        }
      } catch (e) {
        debugPrint('[Bidding WS Error]: $e');
      }
    });
  }

  @override
  void dispose() {
    _wsSubscription?.cancel();
    _animationController.dispose();
    _customFareController.dispose();
    super.dispose();
  }

  IconData _getVehicleIcon(String type) {
    switch (type.toLowerCase()) {
      case 'bike':
        return Icons.two_wheeler_rounded;
      case 'rickshaw':
        return Icons.electric_rickshaw_rounded;
      case 'car':
        return Icons.directions_car_rounded;
      default:
        return Icons.directions_car_rounded;
    }
  }

  Future<void> _startBiddingProcess() async {
    setState(() {
      _biddingStatus = 'searching';
      _riderOffers = [];
    });

    try {
      // Step 1: create the ride so we have a tracking_id. The backend's
      // POST /rides endpoint returns {tracking_id, ...}.
      final rideResp = await ApiClient().post('/rides/', {
        'customer_tracking_id':
            SessionRegistry.instance.trackingId ?? 'CUST-UNKNOWN',
        'vehicle_type': _selectedVehicle,
        'service_type': _serviceType,
        'pickup_lat': 31.5204,
        'pickup_lng': 74.3587,
        'dropoff_lat': 31.5600,
        'dropoff_lng': 74.3400,
        'fare_amount': _negotiatedFare,
        'currency': 'PKR',
      });
      final rideTrackingId = (rideResp as Map<String, dynamic>)['tracking_id']?.toString();
      if (rideTrackingId == null) {
        throw Exception('Ride creation did not return a tracking_id');
      }

      // Step 2: post the customer's opening bid on that ride. Riders
      // see this in their app and may submit counter-offers.
      await ApiClient().post('/rides/$rideTrackingId/bid', {
        'rider_tracking_id': '',
        'proposed_fare': _negotiatedFare,
        'eta_minutes': 5,
        'note': 'Customer posted opening bid via app',
      });
    } catch (e) {
      debugPrint('Bid dispatch error: $e');
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Bid failed: $e')),
        );
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: _animationController,
      builder: (context, child) {
        return Transform.translate(
          offset: Offset(0, _slideAnimation.value * 200),
          child: child,
        );
      },
      child: ClipRRect(
        borderRadius: const BorderRadius.vertical(top: Radius.circular(36)),
        child: BackdropFilter(
          filter: ImageFilter.blur(sigmaX: 20, sigmaY: 20),
          child: Container(
            padding: const EdgeInsets.fromLTRB(24, 16, 24, 24),
            decoration: BoxDecoration(
              color: const Color(0xE6121212), // Deep dark glassmorphism base
              border: Border.all(color: Colors.white.withOpacity(0.08), width: 1.5),
              borderRadius: const BorderRadius.vertical(top: Radius.circular(36)),
            ),
            child: _buildBiddingContent(),
          ),
        ),
      ),
    );
  }

  Widget _buildBiddingContent() {
    if (_biddingStatus == 'searching') {
      return Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          const SizedBox(height: 20),
          const SizedBox(
            width: 80,
            height: 80,
            child: CircularProgressIndicator(
              color: Color(0xFFCAFF33),
              strokeWidth: 4,
            ),
          ),
          const SizedBox(height: 30),
          const Text(
            'Negotiating Fare with Riders...',
            style: TextStyle(color: Colors.white, fontSize: 18, fontWeight: FontWeight.bold, fontFamily: 'Outfit'),
          ),
          const SizedBox(height: 12),
          Text(
            'Your Offer: PKR ${_negotiatedFare.toStringAsFixed(0)} • Sending to nearby riders',
            style: const TextStyle(color: Colors.white70, fontSize: 14, fontFamily: 'Outfit'),
          ),
          const SizedBox(height: 40),
          ElevatedButton(
            onPressed: () {
              setState(() {
                _biddingStatus = 'idle';
              });
            },
            style: ElevatedButton.styleFrom(
              backgroundColor: Colors.white12,
              foregroundColor: Colors.white,
              minimumSize: const Size(double.infinity, 50),
              shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
            ),
            child: const Text('Cancel Request'),
          ),
        ],
      );
    }

    if (_biddingStatus == 'offers_received') {
      return Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            mainAxisAlignment: MainAxisAlignment.spaceBetween,
            children: [
              const Text(
                'Rider Offers Received',
                style: TextStyle(fontSize: 20, fontWeight: FontWeight.bold, color: Colors.white, fontFamily: 'Outfit'),
              ),
              IconButton(
                icon: const Icon(Icons.close, color: Colors.white70),
                onPressed: () => setState(() => _biddingStatus = 'idle'),
              ),
            ],
          ),
          const SizedBox(height: 8),
          const Text(
            'Riders have responded to your bid. Choose an offer to confirm:',
            style: TextStyle(color: Colors.white60, fontSize: 13, fontFamily: 'Outfit'),
          ),
          const SizedBox(height: 20),
          ConstrainedBox(
            constraints: const BoxConstraints(maxHeight: 300),
            child: ListView.separated(
              shrinkWrap: true,
              itemCount: _riderOffers.length,
              separatorBuilder: (context, idx) => const SizedBox(height: 12),
              itemBuilder: (context, idx) {
                final offer = _riderOffers[idx];
                return Container(
                  padding: const EdgeInsets.all(16),
                  decoration: BoxDecoration(
                    color: Colors.white.withOpacity(0.04),
                    borderRadius: BorderRadius.circular(20),
                    border: Border.all(color: Colors.white.withOpacity(0.08)),
                  ),
                  child: Row(
                    children: [
                      CircleAvatar(
                        backgroundColor: const Color(0xFFCAFF33).withOpacity(0.15),
                        child: const Icon(Icons.person, color: Color(0xFFCAFF33)),
                      ),
                      const SizedBox(width: 14),
                      Expanded(
                        child: Column(
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            Text(
                              offer['rider_name'] as String,
                              style: const TextStyle(color: Colors.white, fontWeight: FontWeight.bold, fontSize: 14),
                            ),
                            const SizedBox(height: 2),
                            Text(
                              '${offer['rating']} • ${offer['plate']} • ETA: ${offer['eta']}',
                              style: const TextStyle(color: Colors.white54, fontSize: 11),
                            ),
                          ],
                        ),
                      ),
                      Column(
                        crossAxisAlignment: CrossAxisAlignment.end,
                        children: [
                          Text(
                            'PKR ${offer['fare'].toStringAsFixed(0)}',
                            style: const TextStyle(color: Color(0xFFCAFF33), fontWeight: FontWeight.w900, fontSize: 16),
                          ),
                          const SizedBox(height: 6),
                          ElevatedButton(
                            onPressed: () {
                              Navigator.pop(context);
                              widget.onBookRide(_selectedVehicle, offer['fare'] as double);
                            },
                            style: ElevatedButton.styleFrom(
                              backgroundColor: const Color(0xFFCAFF33),
                              foregroundColor: Colors.black,
                              padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 8),
                              minimumSize: Size.zero,
                              shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(10)),
                            ),
                            child: const Text('Accept', style: TextStyle(fontWeight: FontWeight.bold, fontSize: 11)),
                          ),
                        ],
                      ),
                    ],
                  ),
                );
              },
            ),
          ),
          const SizedBox(height: 20),
        ],
      );
    }

    return Column(
      mainAxisSize: MainAxisSize.min,
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Center(
          child: Container(
            width: 48,
            height: 5,
            decoration: BoxDecoration(
              color: Colors.white.withOpacity(0.2),
              borderRadius: BorderRadius.circular(10),
            ),
          ),
        ),
        const SizedBox(height: 20),
        // Passenger vs Courier toggle selector
        Row(
          children: [
            Expanded(
              child: GestureDetector(
                onTap: () => setState(() => _serviceType = 'passenger'),
                child: Container(
                  padding: const EdgeInsets.symmetric(vertical: 12),
                  decoration: BoxDecoration(
                    color: _serviceType == 'passenger' ? const Color(0xFFCAFF33) : Colors.white.withOpacity(0.05),
                    borderRadius: BorderRadius.circular(15),
                  ),
                  child: Row(
                    mainAxisAlignment: MainAxisAlignment.center,
                    children: [
                      Icon(Icons.directions_run_rounded, color: _serviceType == 'passenger' ? Colors.black : Colors.white),
                      const SizedBox(width: 8),
                      Text(
                        'Passenger Ride',
                        style: TextStyle(
                          color: _serviceType == 'passenger' ? Colors.black : Colors.white,
                          fontWeight: FontWeight.bold,
                          fontSize: 13,
                        ),
                      ),
                    ],
                  ),
                ),
              ),
            ),
            const SizedBox(width: 12),
            Expanded(
              child: GestureDetector(
                onTap: () => setState(() => _serviceType = 'courier'),
                child: Container(
                  padding: const EdgeInsets.symmetric(vertical: 12),
                  decoration: BoxDecoration(
                    color: _serviceType == 'courier' ? const Color(0xFFCAFF33) : Colors.white.withOpacity(0.05),
                    borderRadius: BorderRadius.circular(15),
                  ),
                  child: Row(
                    mainAxisAlignment: MainAxisAlignment.center,
                    children: [
                      Icon(Icons.mark_as_unread_rounded, color: _serviceType == 'courier' ? Colors.black : Colors.white),
                      const SizedBox(width: 8),
                      Text(
                        'Courier / Parcel',
                        style: TextStyle(
                          color: _serviceType == 'courier' ? Colors.black : Colors.white,
                          fontWeight: FontWeight.bold,
                          fontSize: 13,
                        ),
                      ),
                    ],
                  ),
                ),
              ),
            ),
          ],
        ),
        const SizedBox(height: 24),
        Text(
          _serviceType == 'passenger' ? 'Select Ride Option' : 'Select Package Vehicle Delivery',
          style: const TextStyle(
            fontSize: 18,
            fontWeight: FontWeight.bold,
            color: Colors.white,
            fontFamily: 'Outfit',
          ),
        ),
        const SizedBox(height: 16),
        // Vehicle Option Cards
        ListView.builder(
          shrinkWrap: true,
          physics: const NeverScrollableScrollPhysics(),
          itemCount: widget.estimates.length,
          itemBuilder: (context, idx) {
            final est = widget.estimates[idx];
            final type = (est['vehicle_type'] as String?) ?? '';
            // For package delivery, we increase the base rate by 20 PKR for weight handling
            final baseFare = (est['total_fare'] as num?)?.toDouble() ?? 0.0;
            final totalFare = _serviceType == 'courier' ? baseFare + 20.0 : baseFare;
            final etaSec = (est['eta_seconds'] as num?)?.toDouble() ?? 0.0;
            final surge = (est['surge_multiplier'] as num?)?.toDouble() ?? 1.0;
            
            final isSelected = _selectedVehicle == type;
            final etaMin = (etaSec / 60.0).round();

            return AnimatedContainer(
              duration: const Duration(milliseconds: 250),
              margin: const EdgeInsets.symmetric(vertical: 6),
              decoration: BoxDecoration(
                color: isSelected 
                    ? const Color(0xFFCAFF33).withOpacity(0.15)
                    : Colors.white.withOpacity(0.03),
                borderRadius: BorderRadius.circular(20),
                border: Border.all(
                  color: isSelected 
                      ? const Color(0xFFCAFF33) 
                      : Colors.white.withOpacity(0.06),
                  width: 2,
                ),
              ),
              child: InkWell(
                borderRadius: BorderRadius.circular(20),
                onTap: () {
                  setState(() {
                    _selectedVehicle = type;
                    _selectedFare = totalFare;
                    if (!_customOfferEnabled) {
                      _negotiatedFare = totalFare;
                      _customFareController.text = totalFare.toStringAsFixed(0);
                    }
                  });
                },
                child: Padding(
                  padding: const EdgeInsets.all(14.0),
                  child: Row(
                    children: [
                      Container(
                        padding: const EdgeInsets.all(10),
                        decoration: BoxDecoration(
                          color: isSelected 
                              ? const Color(0xFFCAFF33) 
                              : Colors.white.withOpacity(0.06),
                          borderRadius: BorderRadius.circular(14),
                        ),
                        child: Icon(
                          _getVehicleIcon(type),
                          color: isSelected ? Colors.black : Colors.white,
                          size: 24,
                        ),
                      ),
                      const SizedBox(width: 14),
                      Expanded(
                        child: Column(
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            Row(
                              children: [
                                Text(
                                  type.toUpperCase(),
                                  style: const TextStyle(
                                    fontWeight: FontWeight.bold,
                                    fontSize: 15,
                                    color: Colors.white,
                                    fontFamily: 'Outfit',
                                  ),
                                ),
                                if (surge > 1.0) ...[
                                  const SizedBox(width: 8),
                                  Container(
                                    padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
                                    decoration: BoxDecoration(
                                      color: Colors.amber.withOpacity(0.2),
                                      borderRadius: BorderRadius.circular(8),
                                      border: Border.all(color: Colors.amber.withOpacity(0.4)),
                                    ),
                                    child: Text(
                                      'Surge ${surge.toStringAsFixed(1)}x',
                                      style: const TextStyle(
                                        color: Colors.amber,
                                        fontSize: 9,
                                        fontWeight: FontWeight.bold,
                                      ),
                                    ),
                                  ),
                                ],
                              ],
                            ),
                            const SizedBox(height: 4),
                            Text(
                              _serviceType == 'passenger' ? 'ETA: $etaMin mins' : 'ETA: $etaMin mins • Courier Delivery',
                              style: TextStyle(
                                color: Colors.white.withOpacity(0.5),
                                fontSize: 11,
                                fontFamily: 'Outfit',
                              ),
                            ),
                          ],
                        ),
                      ),
                      Text(
                        'PKR ${totalFare.toStringAsFixed(0)}',
                        style: const TextStyle(
                          fontWeight: FontWeight.w900,
                          fontSize: 16,
                          color: Color(0xFFCAFF33),
                          fontFamily: 'Outfit',
                        ),
                      ),
                    ],
                  ),
                ),
              ),
            );
          },
        ),
        const SizedBox(height: 16),
        const Divider(color: Colors.white10),
        const SizedBox(height: 10),
        // inDriver style Bidding / Custom Offer Row
        Row(
          mainAxisAlignment: MainAxisAlignment.spaceBetween,
          children: [
            const Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  'Offer Custom Fare (Negotiation)',
                  style: TextStyle(color: Colors.white, fontWeight: FontWeight.bold, fontSize: 13, fontFamily: 'Outfit'),
                ),
                SizedBox(height: 2),
                Text(
                  'Bargain directly with nearby riders',
                  style: TextStyle(color: Colors.white54, fontSize: 11, fontFamily: 'Outfit'),
                ),
              ],
            ),
            Switch(
              value: _customOfferEnabled,
              activeColor: const Color(0xFFCAFF33),
              activeTrackColor: Colors.white10,
              onChanged: (val) {
                setState(() {
                  _customOfferEnabled = val;
                  if (!val) {
                    _negotiatedFare = _selectedFare;
                  }
                });
              },
            ),
          ],
        ),
        if (_customOfferEnabled) ...[
          const SizedBox(height: 12),
          Row(
            mainAxisAlignment: MainAxisAlignment.center,
            children: [
              IconButton(
                onPressed: _negotiatedFare > 50
                    ? () {
                        setState(() {
                          _negotiatedFare -= 10.0;
                          _customFareController.text = _negotiatedFare.toStringAsFixed(0);
                        });
                      }
                    : null,
                icon: const Icon(Icons.remove_circle_outline, color: Color(0xFFCAFF33), size: 30),
              ),
              Container(
                width: 140,
                alignment: Alignment.center,
                child: TextField(
                  controller: _customFareController,
                  keyboardType: TextInputType.number,
                  textAlign: TextAlign.center,
                  style: const TextStyle(color: Color(0xFFCAFF33), fontWeight: FontWeight.w900, fontSize: 24, fontFamily: 'Outfit'),
                  decoration: const InputDecoration(
                    prefixText: 'PKR ',
                    prefixStyle: TextStyle(color: Color(0xFFCAFF33), fontSize: 18),
                    border: InputBorder.none,
                  ),
                  onChanged: (val) {
                    final d = double.tryParse(val) ?? 0.0;
                    setState(() {
                      _negotiatedFare = d;
                    });
                  },
                ),
              ),
              IconButton(
                onPressed: () {
                  setState(() {
                    _negotiatedFare += 10.0;
                    _customFareController.text = _negotiatedFare.toStringAsFixed(0);
                  });
                },
                icon: const Icon(Icons.add_circle_outline, color: Color(0xFFCAFF33), size: 30),
              ),
            ],
          ),
          const SizedBox(height: 4),
          const Center(
            child: Text(
              'Riders can accept, decline or send counter-offers.',
              style: TextStyle(color: Colors.white38, fontSize: 11),
            ),
          ),
        ],
        const SizedBox(height: 24),
        // Confirm Button
        ElevatedButton(
          onPressed: _selectedVehicle.isEmpty || (_customOfferEnabled && _negotiatedFare <= 0)
              ? null
              : () {
                  if (_customOfferEnabled) {
                    _startBiddingProcess();
                  } else {
                    Navigator.pop(context);
                    widget.onBookRide(_selectedVehicle, _selectedFare);
                  }
                },
          style: ElevatedButton.styleFrom(
            backgroundColor: const Color(0xFFCAFF33),
            foregroundColor: Colors.black,
            disabledBackgroundColor: Colors.white.withOpacity(0.1),
            minimumSize: const Size(double.infinity, 56),
            shape: RoundedRectangleBorder(
              borderRadius: BorderRadius.circular(20),
            ),
            elevation: 0,
          ),
          child: Text(
            _customOfferEnabled 
                ? 'Send Bid to Riders (PKR ${_negotiatedFare.toStringAsFixed(0)})'
                : (_serviceType == 'passenger' ? 'Confirm Ride Request' : 'Confirm Package Delivery'),
            style: const TextStyle(
              fontWeight: FontWeight.w900,
              fontSize: 16,
              fontFamily: 'Outfit',
            ),
          ),
        ),
      ],
    );
  }
}
