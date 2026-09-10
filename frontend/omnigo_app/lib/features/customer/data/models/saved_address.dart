import 'package:flutter/material.dart';

class SavedAddress {
  const SavedAddress({
    required this.id,
    required this.label,
    required this.address,
    required this.latitude,
    required this.longitude,
    this.isDefault = false,
  });

  factory SavedAddress.fromJson(Map<String, dynamic> json) => SavedAddress(
        id: (json['id'] ?? '').toString(),
        label: (json['label'] ?? 'Home').toString(),
        address: (json['address'] ?? '').toString(),
        latitude: (json['latitude'] as num?)?.toDouble() ?? 0.0,
        longitude: (json['longitude'] as num?)?.toDouble() ?? 0.0,
        isDefault: json['is_default'] == true,
      );

  final String id;
  final String label; // "Home", "Work", "Family", "Other"
  final String address;
  final double latitude;
  final double longitude;
  final bool isDefault;

  SavedAddress copyWith({
    String? id,
    String? label,
    String? address,
    double? latitude,
    double? longitude,
    bool? isDefault,
  }) =>
      SavedAddress(
        id: id ?? this.id,
        label: label ?? this.label,
        address: address ?? this.address,
        latitude: latitude ?? this.latitude,
        longitude: longitude ?? this.longitude,
        isDefault: isDefault ?? this.isDefault,
      );

  Map<String, dynamic> toJson() => {
        'id': id,
        'label': label,
        'address': address,
        'latitude': latitude,
        'longitude': longitude,
        'is_default': isDefault,
      };

  IconData get icon {
    switch (label.toLowerCase()) {
      case 'home':
        return Icons.home_rounded;
      case 'work':
      case 'office':
        return Icons.work_rounded;
      case 'family':
      case 'parents':
        return Icons.people_rounded;
      default:
        return Icons.location_on_rounded;
    }
  }
}
