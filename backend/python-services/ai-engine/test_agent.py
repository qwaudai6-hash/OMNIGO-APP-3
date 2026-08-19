import sys
import os

sys.path.append("/run/media/phatan/New Volume/OMNIGO E COMMERCE APP/backend/python-services/ai-engine")

from api.agents.concierge_agent import run_agent

queries = [
    "I am leaving the office now. Book a ride home and order some pizza for me.",
    "Can you check my calendar to see when I am free?",
    "I just want a ride to the airport, please.",
    "Order some coffee for my meeting."
]

print("=== OMNIGO 2027 AGENTIC ORCHESTRATOR EXPLORATION ===\n")

for i, q in enumerate(queries):
    print(f"--- SCENARIO {i+1} ---")
    print(f"User Prompt: \"{q}\"")
    print("Agent Thinking...")
    
    state = run_agent(q)
    
    intent = state['analyzed_intent']
    print(f" > Intent Analyzed: {intent}")
    print(" > Tools Decided:")
    if not state["tools_to_call"]:
        print("    (None)")
    for t in state["tools_to_call"]:
        print(f"    - {t['name']}(args: {t['args']})")
        
    print(" > Tool Execution Results:")
    if not state["tool_results"]:
        print("    (None)")
    for r in state["tool_results"]:
        tname = r['tool']
        tstatus = r['result']['status']
        taction = r['result'].get('action', 'checked')
        print(f"    - {tname}: {tstatus} (Action: {taction})")
        
    final_resp = state['final_response'].strip()
    print(f" > Final Synthesized Response:\n   {final_resp}")
    print("\n" + "="*50 + "\n")
