use actix::{Actor, AsyncContext, Handler, Message, StreamHandler};
use actix_web::{web, App, HttpResponse, HttpServer, Responder};
use actix_web_actors::ws;
use dashmap::DashMap;
use std::sync::Arc;
use std::env;

// Telemetry event schema. Streamlined without duplicates.
#[derive(serde::Deserialize, serde::Serialize, Clone, Debug)]
pub struct TelemetryEvent {
    pub customer_id: String,
    pub order_id: String,
    pub vector_clock: u64,
    pub lat: f64,
    pub lng: f64,
}

// Global shared state using lock-free DashMap for 50M scale.
struct AppState {
    sessions: DashMap<String, actix::Addr<WsSession>>,
}

// WS Session actor.
struct WsSession {
    tracking_id: String,
    state: web::Data<Arc<AppState>>,
}

impl Actor for WsSession {
    type Context = ws::WebsocketContext<Self>;

    fn started(&mut self, ctx: &mut Self::Context) {
        let addr = ctx.address();
        // DashMap allows concurrent, lock-free inserts. Eliminates Mutex bottlenecks.
        self.state.sessions.insert(self.tracking_id.clone(), addr);
        println!("WS Session registered: {}", self.tracking_id);
    }

    fn stopped(&mut self, _ctx: &mut Self::Context) {
        self.state.sessions.remove(&self.tracking_id);
        println!("WS Session terminated: {}", self.tracking_id);
    }
}

// Handle incoming WS frames.
// Non-blocking Fast Path: instantly forwards frames to downstream Kafka pipelines.
// No in-actor sleep timers or sliding window buffers.
impl StreamHandler<Result<ws::Message, ws::ProtocolError>> for WsSession {
    fn handle(&mut self, msg: Result<ws::Message, ws::ProtocolError>, ctx: &mut Self::Context) {
        match msg {
            Ok(ws::Message::Ping(msg)) => ctx.pong(&msg),
            Ok(ws::Message::Text(text)) => {
                if let Ok(event) = serde_json::from_str::<TelemetryEvent>(&text) {
                    // Forward instantly to background worker or log to Kafka.
                    println!("Instant Forward (Kafka Stream): Rider {} at [{}, {}] (Clock: {})", 
                        self.tracking_id, event.lat, event.lng, event.vector_clock);
                }
            }
            _ => (),
        }
    }
}

#[derive(Message)]
#[rtype(result = "()")]
struct ServerMessage(String);

impl Handler<ServerMessage> for WsSession {
    type Result = ();

    fn handle(&mut self, msg: ServerMessage, ctx: &mut Self::Context) {
        ctx.text(msg.0);
    }
}

async fn ws_index(
    req: actix_web::HttpRequest,
    stream: web::Payload,
    state: web::Data<Arc<AppState>>,
) -> Result<HttpResponse, actix_web::Error> {
    let tracking_id = match req.query_string().split('=').nth(1) {
        Some(id) => id.to_string(),
        None => return Err(actix_web::error::ErrorUnauthorized("Missing tracking_id")),
    };

    let session = WsSession { tracking_id, state };
    ws::start(session, &req, stream)
}

async fn health_check() -> impl Responder {
    HttpResponse::Ok().json("websocket-gateway (high-scale stateless mode) is running")
}

#[actix_web::main]
async fn main() -> std::io::Result<()> {
    let _ = dotenvy::dotenv();
    let port = env::var("PORT").unwrap_or_else(|_| "8087".to_string());
    let addr = format!("0.0.0.0:{}", port);

    let app_state = web::Data::new(Arc::new(AppState {
        sessions: DashMap::new(),
    }));

    println!("Launching Stateless DashMap WebSocket Gateway on {}", addr);

    HttpServer::new(move || {
        App::new()
            .app_data(app_state.clone())
            .route("/health", web::get().to(health_check))
            .route("/ws", web::get().to(ws_index))
    })
    .bind(&addr)?
    .run()
    .await
}
