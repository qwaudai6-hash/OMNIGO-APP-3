import 'dart:async';
import 'dart:math';

import 'package:flutter/material.dart';
import 'package:maplibre_gl/maplibre_gl.dart';
import 'package:http/http.dart' as http;

import 'package:omnigo_app/core/network/api_endpoints.dart';

/// Data class for a map marker.
class MarkerData {
  const MarkerData({
    required this.position,
    this.iconImage,
    this.iconSize = 1.0,
    this.properties,
  });

  final LatLng position;
  final String? iconImage;
  final double iconSize;
  final Map<String, dynamic>? properties;
}

/// Centralised, reusable MapLibre map widget used by customer, vendor and
/// rider screens. It proxies all tiles/glyphs/sprites through the internal
/// Go map-service so the MapTiler (or future self-hosted) API key never ships
/// inside the mobile binary.
class MapLibreMapWidget extends StatefulWidget {
  const MapLibreMapWidget({
    super.key,
    this.initialCenter,
    this.initialZoom = 14.0,
    this.markers = const {},
    this.polylines = const [],
    this.myLocationEnabled = true,
    this.myLocationTrackingMode = MyLocationTrackingMode.Tracking,
    this.onMapCreated,
    this.onStyleLoaded,
    this.onMapClick,
    this.onUserLocationUpdated,
    this.showUserDot = true,
    this.padding,
    this.styleUrl,
  });

  final LatLng? initialCenter;
  final double initialZoom;

  /// Map of marker id -> (position, iconImageName). Icons must be added to
  /// the style by the caller or via [addImageFromAsset].
  final Map<String, MarkerData> markers;

  /// List of polylines to render on the map.
  final List<List<LatLng>> polylines;

  final bool myLocationEnabled;
  final MyLocationTrackingMode myLocationTrackingMode;
  final bool showUserDot;
  final EdgeInsets? padding;

  /// Optional override. Defaults to the internal map-service style endpoint.
  final String? styleUrl;

  final void Function(MaplibreMapController controller)? onMapCreated;
  final VoidCallback? onStyleLoaded;
  final void Function(Point<double>, LatLng)? onMapClick;
  final OnUserLocationUpdated? onUserLocationUpdated;

  @override
  State<MapLibreMapWidget> createState() => _MapLibreMapWidgetState();
}

class _MapLibreMapWidgetState extends State<MapLibreMapWidget> {
  MaplibreMapController? _controller;
  bool _styleLoaded = false;

  String get _styleUrl =>
      widget.styleUrl ?? ApiEndpoints.mapStyle();

  @override
  void didUpdateWidget(covariant MapLibreMapWidget oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (_controller != null && _styleLoaded) {
      _updateMarkersAndPolylines(oldWidget);
    }
  }

  void _updateMarkersAndPolylines(covariant MapLibreMapWidget oldWidget) {
    if (widget.markers != oldWidget.markers) {
      _syncMarkers();
    }
    if (widget.polylines != oldWidget.polylines) {
      _syncPolylines();
    }
  }

  Future<void> _onMapCreated(MaplibreMapController controller) async {
    _controller = controller;
    widget.onMapCreated?.call(controller);
    controller.onSymbolTapped.add(_onSymbolTapped);
  }

  void _onStyleLoaded() {
    _styleLoaded = true;
    _syncMarkers();
    _syncPolylines();
    widget.onStyleLoaded?.call();
  }

  void _onSymbolTapped(Symbol symbol) {
    debugPrint('MapLibre symbol tapped: ${symbol.id}');
  }

  Future<void> _syncMarkers() async {
    final controller = _controller;
    if (controller == null) return;

    final symbols = controller.symbols;
    for (final s in symbols) {
      await controller.removeSymbol(s);
    }

    for (final entry in widget.markers.entries) {
      final data = entry.value;
      final options = SymbolOptions(
        geometry: data.position,
        iconImage: data.iconImage,
        iconSize: data.iconSize,
      );
      await controller.addSymbol(
        options,
        data.properties ?? {'id': entry.key},
      );
    }
  }

  Future<void> _syncPolylines() async {
    final controller = _controller;
    if (controller == null) return;

    final lines = controller.lines;
    for (final l in lines) {
      await controller.removeLine(l);
    }

    for (final points in widget.polylines) {
      await controller.addLine(
        LineOptions(
          geometry: points,
          lineColor: '#CAFF33',
          lineWidth: 5.0,
        ),
      );
    }
  }

  Future<void> animateCamera(LatLng target, {double? zoom}) async {
    final controller = _controller;
    if (controller == null) return;
    await controller.animateCamera(
      CameraUpdate.newLatLngZoom(target, zoom ?? widget.initialZoom),
    );
  }

  Future<void> fitBounds(List<LatLng> points, {double padding = 50}) async {
    final controller = _controller;
    if (controller == null || points.length < 2) return;
    double minLat = 90, maxLat = -90, minLng = 180, maxLng = -180;
    for (final p in points) {
      minLat = min(minLat, p.latitude);
      maxLat = max(maxLat, p.latitude);
      minLng = min(minLng, p.longitude);
      maxLng = max(maxLng, p.longitude);
    }
    await controller.animateCamera(
      CameraUpdate.newLatLngBounds(
        LatLngBounds(
          southwest: LatLng(minLat, minLng),
          northeast: LatLng(maxLat, maxLng),
        ),
        left: padding,
        top: padding,
        right: padding,
        bottom: padding,
      ),
    );
  }

  @override
  void dispose() {
    _controller?.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return MaplibreMap(
      styleString: _styleUrl,
      initialCameraPosition: CameraPosition(
        target: widget.initialCenter ?? const LatLng(31.5204, 74.3587), // Lahore
        zoom: widget.initialZoom,
      ),
      myLocationEnabled: widget.myLocationEnabled,
      myLocationTrackingMode: widget.myLocationTrackingMode,
      trackCameraPosition: true,
      compassEnabled: true,
      compassViewMargins: const Point(16, 80),
      onMapCreated: _onMapCreated,
      onStyleLoadedCallback: _onStyleLoaded,
      onMapClick: widget.onMapClick,
      annotationOrder: const [AnnotationType.line, AnnotationType.symbol],
      cameraTargetBounds: CameraTargetBounds.unbounded,
    );
  }
}

/// Helper to load a remote PNG icon into the map style at runtime.
Future<void> addNetworkImageToMap(
  MaplibreMapController controller,
  String name,
  String url,
) async {
  try {
    final response = await http.get(Uri.parse(url));
    if (response.statusCode == 200) {
      await controller.addImage(name, response.bodyBytes);
    }
  } catch (e) {
    debugPrint('Failed to load map image $name: $e');
  }
}
