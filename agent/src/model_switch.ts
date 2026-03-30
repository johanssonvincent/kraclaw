export interface AgentState {
  currentModel: string;
  sessionId?: string;
}

export function applySetModel(state: AgentState, model: string): AgentState {
  return {
    currentModel: model,
    sessionId: undefined,
  };
}
