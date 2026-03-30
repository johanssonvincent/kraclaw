import { query } from "@anthropic-ai/claude-agent-sdk";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import { IPCClient } from "./ipc.js";
import { applySetModel } from "./model_switch.js";
import type { IPCMessage } from "./types.js";

const REDIS_URL = process.env.REDIS_URL ?? "redis://localhost:6379";
let GROUP_FOLDER = process.env.GROUP_FOLDER ?? "";

// Parse CLI arguments for --group
for (let i = 0; i < process.argv.length; i++) {
  if (process.argv[i] === "--group" && i + 1 < process.argv.length) {
    GROUP_FOLDER = process.argv[i + 1];
    break;
  }
}

const CHAT_JID = process.env.CHAT_JID ?? "";
const IS_MAIN = process.env.IS_MAIN === "true";
const ASSISTANT_NAME = process.env.ASSISTANT_NAME ?? "Claude";
const INITIAL_SESSION_ID = process.env.SESSION_ID ?? undefined;
const INITIAL_MODEL = process.env.CLAUDE_MODEL ?? "";

const WORKSPACE_DIR = "/workspace";
const CONFIG_DIR = "/config";

if (!GROUP_FOLDER) {
  console.error("GROUP_FOLDER is required");
  process.exit(1);
}

async function initWorkspace(): Promise<void> {
  // Ensure archives directory exists.
  await fs.mkdir(path.join(WORKSPACE_DIR, "archives"), { recursive: true });

  // Seed per-group CLAUDE.md from global template if it doesn't exist.
  const groupClaudeMd = path.join(WORKSPACE_DIR, "CLAUDE.md");
  try {
    await fs.access(groupClaudeMd);
  } catch {
    // No per-group CLAUDE.md yet — try to seed from global template.
    const globalClaudeMd = path.join(CONFIG_DIR, "global-CLAUDE.md");
    try {
      const global = await fs.readFile(globalClaudeMd, "utf-8");
      await fs.writeFile(groupClaudeMd, global);
      console.log("seeded CLAUDE.md from global template");
    } catch {
      await fs.writeFile(
        groupClaudeMd,
        `# ${GROUP_FOLDER}\n\nGroup memory and instructions.\n`
      );
      console.log("created default CLAUDE.md");
    }
  }
}

async function archiveConversation(
  sessionId: string | undefined,
  input: string,
  response: string
): Promise<void> {
  const timestamp = new Date().toISOString().replace(/[:.]/g, "-");
  const archivePath = path.join(WORKSPACE_DIR, "archives", `${timestamp}.md`);

  const content = [
    `# Conversation Archive`,
    `**Session:** ${sessionId ?? "unknown"}`,
    `**Timestamp:** ${new Date().toISOString()}`,
    `**Group:** ${GROUP_FOLDER}`,
    "",
    "## Input",
    input,
    "",
    "## Response",
    response,
  ].join("\n");

  await fs.writeFile(archivePath, content);
}

async function main(): Promise<void> {
  console.log("agent starting", { GROUP_FOLDER, CHAT_JID, IS_MAIN, ASSISTANT_NAME });

  await initWorkspace();

  const ipc = new IPCClient(REDIS_URL, GROUP_FOLDER);
  let sessionId: string | undefined = INITIAL_SESSION_ID;
  let currentModel = INITIAL_MODEL;
  let running = true;

  // Graceful shutdown: notify orchestrator so the group gets marked inactive.
  let shuttingDown = false;
  const shutdown = async () => {
    if (shuttingDown) return;
    shuttingDown = true;
    console.log("shutdown signal received, notifying orchestrator");
    running = false;
    try {
      await ipc.publishOutput({
        group: GROUP_FOLDER,
        type: "shutdown",
        payload: {},
      });
    } catch (err) {
      console.error("failed to publish shutdown", err);
    }
    await ipc.close();
    process.exit(0);
  };
  process.on("SIGTERM", shutdown);
  process.on("SIGINT", shutdown);

  const systemPrompt = buildSystemPrompt();

  while (running) {
    // Check close signal.
    const shouldClose = await ipc.checkClose();
    if (shouldClose) {
      console.log("close signal received, shutting down");
      break;
    }

    // Read input from IPC.
    const input = await ipc.readInput();
    if (!input) {
      continue;
    }

    console.log("received input", { type: input.type, id: input.id });

    if (input.type === "set_model") {
      const payload = input.payload as { model?: string };
      if (payload.model) {
        const nextState = applySetModel({ currentModel, sessionId }, payload.model);
        currentModel = nextState.currentModel;
        sessionId = nextState.sessionId;
        console.log("model updated", { model: currentModel, sessionReset: true });
      }
      continue;
    }

    if (input.type !== "message") {
      continue;
    }

    const payload = input.payload as { messages?: string };
    const messages = payload.messages ?? "";
    if (!messages) {
      continue;
    }

    try {
      const result = await processMessage(ipc, messages, systemPrompt, sessionId, currentModel);
      if (result.sessionId) {
        sessionId = result.sessionId;
      }
      // Archive the conversation for long-term memory.
      await archiveConversation(sessionId, messages, result.responseText).catch(
        (err) => console.error("failed to archive conversation", err)
      );
    } catch (err) {
      console.error("failed to process message", err);
      await ipc.publishOutput({
        group: GROUP_FOLDER,
        type: "message",
        payload: { text: "I encountered an error processing your message. Please try again." },
      });
    }
  }

  await ipc.close();
  console.log("agent shutdown complete");
}

interface ProcessResult {
  sessionId?: string;
  responseText: string;
}

function buildQueryOptions(systemPrompt: string, model: string): Record<string, unknown> {
  const options: Record<string, unknown> = {
    systemPrompt,
    cwd: WORKSPACE_DIR,
    settingSources: ["project"],
    permissionMode: "bypassPermissions",
    allowDangerouslySkipPermissions: true,
    allowedTools: ["Read", "Write", "Edit", "Bash", "Glob", "Grep"],
    maxTurns: 25,
    debug: true,
    stderr: (data: string) => {
      console.error("[claude-code stderr]", data);
    },
  };

  if (model) {
    options.model = model;
  }
  return options;
}

// queryWithTimeout wraps query() with a timeout for the first message.
// If no message arrives within the timeout, it aborts and returns null.
async function queryWithTimeout(
  prompt: string,
  options: Record<string, unknown>,
  timeoutMs: number
): Promise<AsyncIterable<any> | null> {
  const iter = query({ prompt, options: options as any });
  const asyncIter = iter[Symbol.asyncIterator]();

  // Race the first message against a timeout.
  const first = await Promise.race([
    asyncIter.next(),
    new Promise<null>((resolve) => setTimeout(() => resolve(null), timeoutMs)),
  ]);

  if (first === null || (first as IteratorResult<any>).done) {
    return null;
  }

  // Yield the first message, then continue the iterator.
  const firstResult = first as IteratorResult<any>;
  return (async function* () {
    yield firstResult.value;
    for await (const msg of { [Symbol.asyncIterator]: () => asyncIter }) {
      yield msg;
    }
  })();
}

async function processMessage(
  ipc: IPCClient,
  messages: string,
  systemPrompt: string,
  sessionId: string | undefined,
  model: string
): Promise<ProcessResult> {
  const prompt = messages;
  const result: ProcessResult = { responseText: "" };

  const options = buildQueryOptions(systemPrompt, model);

  if (sessionId) {
    options.resume = sessionId;
    console.log("resuming session", { sessionId });
  }

  console.log("calling query", {
    resume: !!sessionId,
    ANTHROPIC_BASE_URL: process.env.ANTHROPIC_BASE_URL,
  });

  // Try query with a 120s timeout for the first message.
  // Claude with extended thinking can take >60s before the first token.
  // If resume hangs, retry without resume.
  let stream = await queryWithTimeout(prompt, options, 120_000);
  if (!stream && sessionId) {
    console.warn("session resume timed out, retrying without resume");
    const retryOptions = buildQueryOptions(systemPrompt, model);
    stream = await queryWithTimeout(prompt, retryOptions, 120_000);
  }
  if (!stream) {
    console.error("query produced no output");
    return result;
  }

  for await (const message of stream) {
    // Capture session ID from init message.
    if (message.type === "system" && "subtype" in message && message.subtype === "init") {
      result.sessionId = message.session_id;

      await ipc.publishOutput({
        group: GROUP_FOLDER,
        type: "session_update",
        payload: { sessionId: message.session_id },
      });
    }

    // Extract text from assistant messages.
    if (message.type === "assistant" && "message" in message) {
      const assistantMsg = message.message as {
        content?: Array<{ type: string; text?: string }>;
      };

      if (assistantMsg.content) {
        for (const block of assistantMsg.content) {
          if (block.type === "text" && block.text) {
            result.responseText += block.text + "\n";
            await ipc.publishOutput({
              group: GROUP_FOLDER,
              type: "message",
              payload: { text: block.text },
            });
          }
        }
      }
    }

    // Capture session ID from result message.
    if (message.type === "result" && "session_id" in message) {
      result.sessionId = message.session_id;

      await ipc.publishOutput({
        group: GROUP_FOLDER,
        type: "session_update",
        payload: { sessionId: message.session_id },
      });
    }
  }

  return result;
}

function buildSystemPrompt(): string {
  const parts: string[] = [];

  parts.push(`You are ${ASSISTANT_NAME}, an AI assistant in a group chat.`);
  parts.push("You receive messages from the group and respond helpfully.");
  parts.push("Messages are formatted with sender names in XML tags.");
  parts.push("");
  parts.push("Guidelines:");
  parts.push("- Be concise and helpful in your responses.");
  parts.push("- When asked to perform tasks, use the available tools.");
  parts.push("- Respond naturally as a participant in the conversation.");

  if (IS_MAIN) {
    parts.push("- You are the main assistant for this group and should respond to all messages.");
  } else {
    parts.push("- You are a specialized assistant. Only respond when your expertise is relevant.");
  }

  return parts.join("\n");
}

main().catch((err) => {
  console.error("fatal error", err);
  process.exit(1);
});
