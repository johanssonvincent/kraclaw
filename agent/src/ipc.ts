import {
  connect,
  NatsConnection,
  RetentionPolicy,
  StorageType,
  DeliverPolicy,
  AckPolicy,
} from "nats";
import { createHash } from "crypto";
import type { IPCMessage } from "./types.js";
import type { JetStreamClient, JetStreamManager } from "nats";

// Max age for IPC stream: 1 hour (must match server-side NATSBroker)
const IPC_STREAM_MAX_AGE = 60 * 60 * 1_000_000_000; // nanoseconds

// Sanitize group JID: first 16 bytes of SHA-256 hex (32 hex chars total)
function sanitizeGroupJID(jid: string): string {
  const hash = createHash("sha256").update(jid).digest("hex");
  return hash.substring(0, 32); // 16 bytes = 32 hex chars
}

// Sanitize agent ID: replace non-alphanumeric/dash/underscore with underscore, max 32 chars
function sanitizeAgentID(id: string): string {
  let safe = id.replace(/[^a-zA-Z0-9_-]/g, "_");
  if (safe.length > 32) {
    safe = safe.substring(0, 32);
  }
  return safe;
}

export class IPCClient {
  private nc: NatsConnection | null = null;
  private jsClient: JetStreamClient | null = null;
  private jsManager: JetStreamManager | null = null;
  private groupJID: string;
  private agentID: string;
  private natsUrl: string;

  constructor(natsUrl: string, groupJID: string, agentID: string = "node") {
    this.natsUrl = natsUrl;
    this.groupJID = groupJID;
    this.agentID = agentID;
  }

  // Establish connection to NATS server
  async connect(): Promise<void> {
    if (this.nc !== null) {
      return;
    }

    try {
      this.nc = await connect({
        servers: [this.natsUrl],
      });
    } catch (err) {
      throw new Error(
        `ipc client: failed to connect to NATS: ${err instanceof Error ? err.message : String(err)}`
      );
    }

    try {
      this.jsClient = this.nc.jetstream();
      this.jsManager = await this.nc.jetstreamManager();
    } catch (err) {
      if (this.nc) await this.nc.close();
      this.nc = null;
      throw new Error(
        `ipc client: failed to create jetstream client: ${err instanceof Error ? err.message : String(err)}`
      );
    }
  }

  private sanitized(): string {
    return sanitizeGroupJID(this.groupJID);
  }

  private streamName(): string {
    return "KRACLAW_IPC_" + this.sanitized().toUpperCase();
  }

  private inputSubject(): string {
    return (
      "kraclaw.ipc." +
      this.sanitized() +
      "." +
      sanitizeAgentID(this.agentID) +
      ".input"
    );
  }

  private outputSubject(): string {
    return (
      "kraclaw.ipc." +
      this.sanitized() +
      "." +
      sanitizeAgentID(this.agentID) +
      ".output"
    );
  }

  // ensureStream creates the IPC stream if it does not exist.
  // The server creates it first, but the agent calls this defensively.
  private async ensureStream(): Promise<void> {
    if (!this.jsManager) {
      throw new Error("jetstream manager not initialized");
    }

    const sanitized = this.sanitized();
    const streamName = this.streamName();

    try {
      // Try to get existing stream
      await this.jsManager.streams.info(streamName);
    } catch (err) {
      // Stream doesn't exist, create it
      try {
        await this.jsManager.streams.add({
          name: streamName,
          subjects: [
            "kraclaw.ipc." + sanitized + ".*.input",
            "kraclaw.ipc." + sanitized + ".*.output",
          ],
          retention: RetentionPolicy.Limits, // LimitsPolicy: respects MaxAge
          storage: StorageType.File,
          max_age: IPC_STREAM_MAX_AGE,
          num_replicas: 1,
        });
      } catch (createErr) {
        // Race condition: stream was created by server or another agent instance
        if (
          !(
            String(createErr).includes("stream already exists") ||
            String(createErr).includes("STREAM_EXISTS")
          )
        ) {
          throw createErr;
        }
      }
    }
  }

  // readInput reads the next input message with timeout
  async readInput(): Promise<IPCMessage | null> {
    if (!this.jsClient || !this.jsManager) {
      throw new Error("jetstream client not initialized - call connect() first");
    }

    await this.ensureStream();

    const streamName = this.streamName();
    const consumerName = "agent-" + sanitizeAgentID(this.agentID);

    // Create or update durable consumer with DeliverAllPolicy
    try {
      // Try to get existing consumer
      await this.jsManager.consumers.info(streamName, consumerName);
    } catch {
      // Create new consumer if it doesn't exist
      try {
        await this.jsManager.consumers.add(streamName, {
          durable_name: consumerName,
          filter_subject: this.inputSubject(),
          deliver_policy: DeliverPolicy.All, // DeliverAllPolicy: receive all messages from stream history
          ack_policy: AckPolicy.Explicit,
        });
      } catch (createErr) {
        // Race condition: consumer was created by another agent instance
        if (
          !(
            String(createErr).includes("consumer already exists") ||
            String(createErr).includes("CONSUMER_EXISTS")
          )
        ) {
          throw createErr;
        }
      }
    }

    try {
      const sub = await this.jsClient.subscribe(this.inputSubject(), {
        config: {
          durable_name: consumerName,
        },
      });

      try {
        // Set a 5 second timeout for reading
        const timeout = new Promise<null>(() => {
          setTimeout(() => null, 5000);
        });

        let firstMessage = true;
        const msgPromise = (async () => {
          for await (const jmsg of sub) {
            // Parse the message
            const data = new TextDecoder().decode(jmsg.data);
            const parsed = JSON.parse(data);

            const msg: IPCMessage = {
              group: this.groupJID,
              type: parsed.type,
              payload: parsed.payload || {},
            };

            // Explicitly acknowledge the message
            if (jmsg.ack) {
              await jmsg.ack();
            }

            return msg;
          }
          return null;
        })();

        // If first message, wait indefinitely; otherwise timeout
        if (firstMessage) {
          firstMessage = false;
          return await msgPromise;
        }

        return await Promise.race([msgPromise, timeout]);
      } finally {
        await sub.unsubscribe();
      }
    } catch (err) {
      throw new Error(
        `ipc read input: ${err instanceof Error ? err.message : String(err)}`
      );
    }
  }

  // checkClose checks if a close signal was sent (stub implementation)
  async checkClose(): Promise<boolean> {
    // This would check for a special "close" message on the input stream
    // For now, return false to indicate no close signal
    return false;
  }

  // publishOutput publishes a message from the agent to the server
  async publishOutput(msg: IPCMessage): Promise<void> {
    if (!this.jsClient) {
      throw new Error("jetstream client not initialized - call connect() first");
    }

    await this.ensureStream();

    try {
      const ipcMsg = {
        group: this.groupJID,
        agent_id: this.agentID,
        type: msg.type,
        timestamp: new Date().toISOString(),
        payload: msg.payload || {},
      };

      const data = new TextEncoder().encode(JSON.stringify(ipcMsg));
      await this.jsClient.publish(this.outputSubject(), data);
    } catch (err) {
      throw new Error(
        `ipc publish output: ${err instanceof Error ? err.message : String(err)}`
      );
    }
  }

  // Close closes the NATS connection
  async close(): Promise<void> {
    if (this.nc) {
      await this.nc.close();
      this.nc = null;
      this.jsClient = null;
      this.jsManager = null;
    }
  }
}
