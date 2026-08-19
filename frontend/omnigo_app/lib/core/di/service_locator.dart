import 'package:get_it/get_it.dart';
import '../network/api_client.dart';
import '../network/websocket_client.dart';

final GetIt sl = GetIt.instance;

void setupServiceLocator() {
  sl.registerLazySingleton<ApiClient>(() => ApiClient());
  sl.registerLazySingleton<WebSocketClient>(() => WebSocketClient());
}
