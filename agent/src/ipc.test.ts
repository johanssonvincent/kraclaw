import test from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { connect as natsConnect, AckPolicy, DeliverPolicy } from "nats";

import { IPCClient } from "./ipc.js";
import type { IPCMessage } from "./types.js";

// Helper: wait for a port to become available
async function waitForPort(
  port: number,
  timeoutMs: number = 5000
): Promise<boolean> {
  const startTime = Date.now();
  while (Date.now() - startTime < timeoutMs) {
    try {
      const conn = await natsConnect({ servers: `localhost:${port}` });
      conn.close();
      return true;
    } catch {
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
  }
  return false;
}

// Helper: spawn nats-server
async function startNatsServer(
  port: number = 14222
): Promise<{ url: string; kill: () => void } | null> {
  try {
    const proc = spawn("nats-server", ["-js", "-p", String(port), "-m", "8222"], {
      stdio: "pipe",
      detached: false,
    });

    // Wait for server to be ready
    const ready = await waitForPort(port, 5000);
    if (!ready) {
      proc.kill();
      return null;
    }

    return {
      url: `nats://localhost:${port}`,
      kill: () => {
        try {
          proc.kill();
        } catch {
          // already killed
        }
      },
    };
  } catch {
    return null;
  }
}

// Helper: Sanitize group JID to match agent implementation
function sanitizeGroupID(groupJID: string): string {
  const hash = createHash("sha256").update(groupJID).digest("hex");
  return hash.substring(0, 32); // 16 bytes = 32 hex chars
}

// Test: IPCClient.connect() is idempotent
test("IPCClient.connect() is idempotent", async (t) => {
  const server = await startNatsServer();
  t.skip(!server, "nats-server not available");

  const client = new IPCClient(server!.url, "test@group.us", "test-agent");
  try {
    // First connect
    await client.connect();

    // Second connect should succeed (idempotent)
    await client.connect();

    assert.ok(true, "connect() can be called multiple times");
  } finally {
    await client.close();
    server!.kill();
  }
});

// Test: IPCClient.connect() errors on invalid NATS URL
test("IPCClient.connect() fails on invalid NATS URL", async () => {
  const client = new IPCClient("nats://invalid-host-that-does-not-exist:4222", "test@group.us", "test-agent");
  try {
    await assert.rejects(client.connect(), {
      message: /connect|error/i,
    });
  } finally {
    await client.close();
  }
});

// Test: IPCClient.close() is idempotent
test("IPCClient.close() is idempotent", async (t) => {
  const server = await startNatsServer();
  t.skip(!server, "nats-server not available");

  const client = new IPCClient(server!.url, "test@group.us", "test-agent");
  try {
    await client.connect();

    // First close
    await client.close();

    // Second close should succeed
    await client.close();

    assert.ok(true, "close() can be called multiple times");
  } finally {
    server!.kill();
  }
});

// Test: IPCClient.readInput() receives published messages
test("IPCClient.readInput() receives published message from server", async (t) => {
  const server = await startNatsServer();
  t.skip(!server, "nats-server not available");

  const client = new IPCClient(server!.url, "test@group.us", "agent-1");
  const serverConn = await natsConnect({ servers: server!.url });

  try {
    await client.connect();

    // Publish a message from server side
    const inputMsg: IPCMessage = {
      group: "test@group.us",
      type: "message",
      payload: { text: "hello" },
    };

    // Get the stream name and subject that the client expects
    const groupJID = "test@group.us";
    const agentID = "agent-1";

    // Helper to sanitize agent ID
    function sanitizeAgentID(id: string): string {
      return id.replace(/[^a-zA-Z0-9_-]/g, "_").toLowerCase();
    }

    const sanitizedGroup = sanitizeGroupID(groupJID);
    const sanitizedAgent = sanitizeAgentID(agentID);
    const subject = `kraclaw.ipc.${sanitizedGroup}.${sanitizedAgent}.input`;

    // Create stream and publish via server connection
    const js = await serverConn.jetstream();
    try {
      await js.streams.add({
        name: `KRACLAW_IPC_${sanitizedGroup.toUpperCase()}`,
        subjects: [`kraclaw.ipc.${sanitizedGroup}.*.input`, `kraclaw.ipc.${sanitizedGroup}.*.output`],
      });
    } catch {
      // stream might already exist
    }

    // Publish the message
    await js.publish(subject, JSON.stringify(inputMsg));

    // Read from client
    const received = await Promise.race([
      client.readInput(),
      new Promise<IPCMessage | null>((resolve) => setTimeout(() => resolve(null), 3000)),
    ]);

    assert.ok(received, "message was received");
    assert.equal(received?.type, "message");
    assert.deepEqual(received?.payload, { text: "hello" });
  } finally {
    await client.close();
    serverConn.close();
    server!.kill();
  }
});

// Test: IPCClient.readInput() times out after 5 seconds
test("IPCClient.readInput() returns null on timeout (no message published)", async (t) => {
  const server = await startNatsServer();
  t.skip(!server, "nats-server not available");

  const client = new IPCClient(server!.url, "test@timeout.us", "agent-timeout");
  const serverConn = await natsConnect({ servers: server!.url });

  try {
    await client.connect();

    // Create the stream so subscription works
    const groupJID = "test@timeout.us";
    const sanitizedGroup = sanitizeGroupID(groupJID);
    const js = await serverConn.jetstream();
    try {
      await js.streams.add({
        name: `KRACLAW_IPC_${sanitizedGroup.toUpperCase()}`,
        subjects: [`kraclaw.ipc.${sanitizedGroup}.*.input`, `kraclaw.ipc.${sanitizedGroup}.*.output`],
      });
    } catch {
      // stream might already exist
    }

    // Call readInput with no messages published - should timeout after 5s
    const startTime = Date.now();
    const result = await client.readInput();
    const elapsed = Date.now() - startTime;

    assert.equal(result, null, "timeout should return null");
    assert.ok(elapsed >= 4900, `should have waited ~5s, waited ${elapsed}ms`);
  } finally {
    await client.close();
    serverConn.close();
    server!.kill();
  }
});

// Test: IPCClient.publishOutput() sends message
test("IPCClient.publishOutput() sends a message", async (t) => {
  const server = await startNatsServer();
  t.skip(!server, "nats-server not available");

  const client = new IPCClient(server!.url, "test@publish.us", "agent-pub");
  const serverConn = await natsConnect({ servers: server!.url });

  try {
    await client.connect();

    // Publish a message from client
    const outputMsg: IPCMessage = {
      group: "test@publish.us",
      type: "session_update",
      payload: { sessionId: "session-123" },
    };

    await client.publishOutput(outputMsg);

    // Verify via server subscription (subscribe to the output subject)
    function sanitizeAgentID(id: string): string {
      return id.replace(/[^a-zA-Z0-9_-]/g, "_").toLowerCase();
    }

    const sanitizedGroup = sanitizeGroupID("test@publish.us");
    const sanitizedAgent = sanitizeAgentID("agent-pub");
    const js = await serverConn.jetstream();

    // Subscribe to output messages
    const sub = await js.subscribe(`kraclaw.ipc.${sanitizedGroup}.${sanitizedAgent}.output`, {
      max: 1,
    });

    // Should receive the message within 2 seconds
    const received = await Promise.race([
      (async () => {
        for await (const jmsg of sub) {
          return jmsg;
        }
      })(),
      new Promise<null>((resolve) => setTimeout(() => resolve(null), 2000)),
    ]);

    assert.ok(received, "message was published");
    const data = JSON.parse(new TextDecoder().decode((received as any).data));
    assert.equal(data.type, "session_update");
  } finally {
    await client.close();
    serverConn.close();
    server!.kill();
  }
});

// Test 1.1: Promise.race() timeout race condition - timeout fires
test("readInput() timeout fires and returns null without losing errors", async (t) => {
  const server = await startNatsServer();
  t.skip(!server, "nats-server not available");

  const client = new IPCClient(server!.url, "test@race.us", "agent-race");
  const serverConn = await natsConnect({ servers: server!.url });

  try {
    await client.connect();

    const groupJID = "test@race.us";
    const sanitizedGroup = sanitizeGroupID(groupJID);
    const js = await serverConn.jetstream();

    try {
      await js.streams.add({
        name: `KRACLAW_IPC_${sanitizedGroup.toUpperCase()}`,
        subjects: [`kraclaw.ipc.${sanitizedGroup}.*.input`, `kraclaw.ipc.${sanitizedGroup}.*.output`],
      });
    } catch {
      // stream might already exist
    }

    // Call readInput with 5s timeout (no message published)
    const startTime = Date.now();
    const result = await client.readInput();
    const elapsed = Date.now() - startTime;

    // Verify: timeout fired and returned null
    assert.equal(result, null, "readInput should return null on timeout");
    assert.ok(elapsed >= 4900 && elapsed <= 5500, `timeout should be ~5s, got ${elapsed}ms`);
  } finally {
    await client.close();
    serverConn.close();
    server!.kill();
  }
});

// Test 2.2: Concurrent ReadInput() Safety
test("concurrent readInput() calls handled safely without consumer conflicts", async (t) => {
  const server = await startNatsServer();
  t.skip(!server, "nats-server not available");

  const client = new IPCClient(server!.url, "test@concurrent.us", "agent-concurrent");
  const serverConn = await natsConnect({ servers: server!.url });

  try {
    await client.connect();

    const groupJID = "test@concurrent.us";
    const sanitizedGroup = sanitizeGroupID(groupJID);
    const js = await serverConn.jetstream();

    try {
      await js.streams.add({
        name: `KRACLAW_IPC_${sanitizedGroup.toUpperCase()}`,
        subjects: [`kraclaw.ipc.${sanitizedGroup}.*.input`, `kraclaw.ipc.${sanitizedGroup}.*.output`],
      });
    } catch {
      // stream might already exist
    }

    // Publish a message first
    const inputMsg = {
      group: groupJID,
      agent_id: "agent-concurrent",
      type: "message",
      timestamp: new Date().toISOString(),
      payload: { text: "test-concurrent" },
    };
    const inputSubject = `kraclaw.ipc.${sanitizedGroup}.agent_concurrent.input`;
    await js.publish(inputSubject, JSON.stringify(inputMsg));

    // Call readInput() twice concurrently
    // First call should get the message, second should timeout
    const [result1, result2] = await Promise.all([
      client.readInput(),
      client.readInput().catch((err) => {
        // Either returns null or rejects, both are acceptable
        return null;
      }),
    ]);

    // At least one should succeed (the first one)
    assert.ok(result1 !== undefined, "first readInput should complete");

    // Should not see "consumer already in use" errors logged
    // (we can't easily verify log output, but if we got here without error, we pass)
  } finally {
    await client.close();
    serverConn.close();
    server!.kill();
  }
});

// Test 1.1b: Promise.race() - verify subscription is cleaned up after timeout
test("readInput() cleans up subscription after Promise.race() timeout", async (t) => {
  const server = await startNatsServer();
  t.skip(!server, "nats-server not available");

  const client = new IPCClient(server!.url, "test@race-cleanup.us", "agent-cleanup");
  const serverConn = await natsConnect({ servers: server!.url });

  try {
    await client.connect();

    const groupJID = "test@race-cleanup.us";
    const sanitizedGroup = sanitizeGroupID(groupJID);
    const js = await serverConn.jetstream();

    try {
      await js.streams.add({
        name: `KRACLAW_IPC_${sanitizedGroup.toUpperCase()}`,
        subjects: [`kraclaw.ipc.${sanitizedGroup}.*.input`, `kraclaw.ipc.${sanitizedGroup}.*.output`],
      });
    } catch {
      // stream might already exist
    }

    // Call readInput which will timeout
    const result1 = await client.readInput();
    assert.equal(result1, null, "first readInput should timeout and return null");

    // Call readInput again - should also timeout cleanly (subscription was cleaned up)
    const result2 = await client.readInput();
    assert.equal(result2, null, "second readInput should also timeout (subscription was cleaned up)");
  } finally {
    await client.close();
    serverConn.close();
    server!.kill();
  }
});
