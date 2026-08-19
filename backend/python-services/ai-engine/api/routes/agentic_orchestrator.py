from fastapi import APIRouter
from pydantic import BaseModel
from typing import Dict, Any
from ..agents.concierge_agent import run_agent

router = APIRouter()

from typing import Dict, Any, Optional

class ContextData(BaseModel):
    user_id: str
    role: str
    order_id: Optional[str] = None
    tracking_id: Optional[str] = None
    status: Optional[str] = None

class ChatRequest(BaseModel):
    query: str
    user_id: str
    context: Optional[ContextData] = None

@router.post("/agent/orchestrate")
def orchestrate_agent(req: ChatRequest) -> Dict[str, Any]:
    """
    Receives a natural language query from the user and orchestrates the Multi-Agent System.
    The agent autonomously plans and executes tools (e.g. book ride + order food).
    """
    print(f"[AgenticOrchestrator] Received query from {req.user_id}: '{req.query}'")
    
    # Run the LangGraph State Graph
    context_dict = req.context.dict() if req.context else None
    final_state = run_agent(req.query, context_dict)
    
    return {
        "user_id": req.user_id,
        "query_received": req.query,
        "analyzed_intent": final_state["analyzed_intent"],
        "tools_executed": final_state["tools_to_call"],
        "tool_results": final_state["tool_results"],
        "agent_response": final_state["final_response"],
        "architecture": "langgraph_multi_agent_sota_2027"
    }
