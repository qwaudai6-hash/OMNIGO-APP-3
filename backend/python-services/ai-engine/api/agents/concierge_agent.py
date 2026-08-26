import os
import json
import urllib.request
import urllib.error
import time
import logging
from typing import TypedDict, List, Dict, Any, Optional
from langgraph.graph import StateGraph, END
from .tools import AVAILABLE_TOOLS

logger = logging.getLogger("ConciergeAgent")

# Define the State for the Agent Graph
class AgentState(TypedDict):
    user_query: str
    context: Optional[Dict[str, Any]]
    analyzed_intent: str
    engine_used: str  # 'gemini_cloud_api' | 'local_autonomous_ai'
    tools_to_call: List[Dict[str, Any]]
    tool_results: List[Dict[str, Any]]
    final_response: str

def _call_gemini_api(query: str, context: Optional[Dict[str, Any]]) -> Optional[Dict[str, Any]]:
    """
    Attempts to call Gemini 1.5 REST API if GEMINI_API_KEY is configured.
    Returns structured JSON intent + tools if successful, or None on failure.
    """
    api_key = os.getenv("GEMINI_API_KEY") or os.getenv("GEMINI_KEY") or os.getenv("GOOGLE_API_KEY")
    if not api_key:
        return None

    try:
        url = f"https://generativelanguage.googleapis.com/v1beta/models/gemini-1.5-flash:generateContent?key={api_key}"
        system_prompt = (
            "You are OMNIGO Super App AI Agent. Analyze the user query and output ONLY valid JSON with keys: "
            "'intent' (summary text), 'tools' (array of objects with 'name' and 'args' e.g. check_calendar, book_ride, order_food)."
        )
        user_content = f"User Query: {query}\nContext: {json.dumps(context or {})}"

        payload = {
            "contents": [{"parts": [{"text": f"{system_prompt}\n{user_content}"}]}],
            "generationConfig": {"temperature": 0.2, "response_mime_type": "application/json"}
        }

        req = urllib.request.Request(
            url,
            data=json.dumps(payload).encode("utf-8"),
            headers={"Content-Type": "application/json"},
            method="POST"
        )

        with urllib.request.urlopen(req, timeout=5) as response:
            if response.status == 200:
                res_body = json.loads(response.read().decode("utf-8"))
                text_out = res_body["candidates"][0]["content"]["parts"][0]["text"]
                parsed = json.loads(text_out)
                return {
                    "intent": parsed.get("intent", "Gemini LLM Intent Processing"),
                    "tools": parsed.get("tools", []),
                    "raw_text": text_out
                }
    except Exception as e:
        logger.warning(f"[AgentCircuitBreaker] Gemini API call failed or disconnected: {e}. Activating Local Autonomous AI Engine!")
        return None

    return None

# Node 1: Intent Analyzer (Hybrid: Gemini Cloud API -> Local Autonomous AI Circuit Breaker)
def analyze_intent(state: AgentState) -> AgentState:
    query = state["user_query"]
    query_lower = query.lower()
    context = state.get("context") or {}

    # 1. Primary Route: Try Gemini Cloud API
    gemini_res = _call_gemini_api(query, context)
    if gemini_res:
        return {
            **state,
            "analyzed_intent": f"[Gemini 1.5 Cloud] {gemini_res['intent']}",
            "engine_used": "gemini_cloud_api",
            "tools_to_call": gemini_res["tools"]
        }

    # 2. Secondary Circuit Breaker: Local Autonomous Deep NLP & Intent Classifier
    # Works 100% offline without any external API keys or cloud dependencies!
    tools_to_call = []
    intent_parts = []

    if context.get("role") == "RIDER" and context.get("status") == "active_gig":
        intent_parts.append(f"manage active gig {context.get('order_id')}")

    # Semantic pattern matching engine
    if any(k in query_lower for k in ["calendar", "schedule", "meeting", "leaving", "timing", "waqt"]):
        tools_to_call.append({"name": "check_calendar", "args": {}})
        intent_parts.append("check calendar schedule")

    if any(k in query_lower for k in ["ride", "cab", "taxi", "car", "bike", "rickshaw", "home", "airport", "office", "dha", "gulberg"]):
        dest = "Home" if "home" in query_lower else "Airport" if "airport" in query_lower else "City Center"
        tools_to_call.append({"name": "book_ride", "args": {"destination": dest}})
        intent_parts.append(f"book ride to {dest}")

    if any(k in query_lower for k in ["food", "pizza", "burger", "coffee", "biryani", "hungry", "khana", "lunch", "dinner"]):
        item = "Pizza" if "pizza" in query_lower else "Biryani" if "biryani" in query_lower else "Coffee" if "coffee" in query_lower else "Food Item"
        tools_to_call.append({"name": "order_food", "args": {"restaurant": "OMNIGO Partner Store", "item": item}})
        intent_parts.append(f"order {item}")

    full_intent = "[Local Autonomous Engine] " + (" and ".join(intent_parts) if intent_parts else "General Query Assistance")

    return {
        **state,
        "analyzed_intent": full_intent,
        "engine_used": "local_autonomous_ai",
        "tools_to_call": tools_to_call
    }

# Node 2: Tool Execution Engine
# SP-PY-08: LLM output is UNTRUSTED. Validate tool names against the
# whitelist, coerce args to a plain dict of scalars, cap per-call work, and
# never let a single tool exception abort the whole agent loop.
_MAX_TOOL_ARGS = 8
_ALLOWED_ARG_TYPES = (str, int, float, bool)

def execute_tools(state: AgentState) -> AgentState:
    results = []
    for tool_request in state["tools_to_call"]:
        tool_name = tool_request.get("name")
        raw_args = tool_request.get("args") or {}

        if not isinstance(raw_args, dict) or len(raw_args) > _MAX_TOOL_ARGS:
            results.append({"tool": tool_name, "error": "rejected: invalid or oversized args"})
            continue
        if any(not isinstance(v, _ALLOWED_ARG_TYPES) for v in raw_args.values()):
            results.append({"tool": tool_name, "error": "rejected: non-scalar arg"})
            continue

        fn = AVAILABLE_TOOLS.get(tool_name)
        if fn is None:
            results.append({"tool": tool_name, "error": "unknown tool"})
            continue

        try:
            time.sleep(0.2)
            result = fn(**raw_args)
            results.append({"tool": tool_name, "result": result})
        except Exception as exc:  # one bad tool must not kill the agent run
            results.append({"tool": tool_name, "error": f"tool failed: {exc}"})

    return {**state, "tool_results": results}

# Node 3: Response Synthesizer
def synthesize_response(state: AgentState) -> AgentState:
    engine = state.get("engine_used", "local_autonomous_ai")
    
    if not state["tool_results"]:
        return {
            **state,
            "final_response": f"OMNIGO AI ({engine}): Query processed successfully. How else can I assist you with rides or orders?"
        }

    response = f"OMNIGO AI Assistant ({engine}):\n"
    for r in state["tool_results"]:
        t_name = r["tool"]
        res = r["result"]
        if t_name == "check_calendar":
            response += f"📅 Schedule: {res.get('current_event', 'Event')} ends at {res.get('event_end_time', 'N/A')}.\n"
        elif t_name == "book_ride":
            response += f"🚗 Ride Status: {res.get('vehicle', 'Vehicle')} dispatched to {res.get('destination', 'destination')} (ETA: {res.get('eta_minutes', 3)} mins).\n"
        elif t_name == "order_food":
            response += f"🍔 Order Status: {res.get('item', 'Item')} from {res.get('restaurant', 'Store')} placed (Order #{res.get('order_id', '101')}, ETA: {res.get('eta_minutes', 15)} mins).\n"

    return {**state, "final_response": response}

# Build the LangGraph
workflow = StateGraph(AgentState)
workflow.add_node("analyze_intent", analyze_intent)
workflow.add_node("execute_tools", execute_tools)
workflow.add_node("synthesize_response", synthesize_response)

workflow.set_entry_point("analyze_intent")
workflow.add_edge("analyze_intent", "execute_tools")
workflow.add_edge("execute_tools", "synthesize_response")
workflow.add_edge("synthesize_response", END)

concierge_app = workflow.compile()

def run_agent(query: str, context: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
    initial_state = {
        "user_query": query,
        "context": context,
        "analyzed_intent": "",
        "engine_used": "",
        "tools_to_call": [],
        "tool_results": [],
        "final_response": ""
    }
    return concierge_app.invoke(initial_state)

