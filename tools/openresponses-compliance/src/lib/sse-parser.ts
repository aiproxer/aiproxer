import { z } from "zod";
import { errorStreamingEventSchema } from "../generated/kubb/zod/errorStreamingEventSchema";
import { responseCompletedStreamingEventSchema } from "../generated/kubb/zod/responseCompletedStreamingEventSchema";
import { responseContentPartAddedStreamingEventSchema } from "../generated/kubb/zod/responseContentPartAddedStreamingEventSchema";
import { responseContentPartDoneStreamingEventSchema } from "../generated/kubb/zod/responseContentPartDoneStreamingEventSchema";
import { responseCreatedStreamingEventSchema } from "../generated/kubb/zod/responseCreatedStreamingEventSchema";
import { responseFailedStreamingEventSchema } from "../generated/kubb/zod/responseFailedStreamingEventSchema";
import { responseFunctionCallArgumentsDeltaStreamingEventSchema } from "../generated/kubb/zod/responseFunctionCallArgumentsDeltaStreamingEventSchema";
import { responseFunctionCallArgumentsDoneStreamingEventSchema } from "../generated/kubb/zod/responseFunctionCallArgumentsDoneStreamingEventSchema";
import { responseIncompleteStreamingEventSchema } from "../generated/kubb/zod/responseIncompleteStreamingEventSchema";
import { responseInProgressStreamingEventSchema } from "../generated/kubb/zod/responseInProgressStreamingEventSchema";
import { responseOutputItemAddedStreamingEventSchema } from "../generated/kubb/zod/responseOutputItemAddedStreamingEventSchema";
import { responseOutputItemDoneStreamingEventSchema } from "../generated/kubb/zod/responseOutputItemDoneStreamingEventSchema";
import { responseOutputTextAnnotationAddedStreamingEventSchema } from "../generated/kubb/zod/responseOutputTextAnnotationAddedStreamingEventSchema";
import { responseOutputTextDeltaStreamingEventSchema } from "../generated/kubb/zod/responseOutputTextDeltaStreamingEventSchema";
import { responseOutputTextDoneStreamingEventSchema } from "../generated/kubb/zod/responseOutputTextDoneStreamingEventSchema";
import { responseQueuedStreamingEventSchema } from "../generated/kubb/zod/responseQueuedStreamingEventSchema";
import { responseReasoningDeltaStreamingEventSchema } from "../generated/kubb/zod/responseReasoningDeltaStreamingEventSchema";
import { responseReasoningDoneStreamingEventSchema } from "../generated/kubb/zod/responseReasoningDoneStreamingEventSchema";
import { responseReasoningSummaryDeltaStreamingEventSchema } from "../generated/kubb/zod/responseReasoningSummaryDeltaStreamingEventSchema";
import { responseReasoningSummaryDoneStreamingEventSchema } from "../generated/kubb/zod/responseReasoningSummaryDoneStreamingEventSchema";
import { responseReasoningSummaryPartAddedStreamingEventSchema } from "../generated/kubb/zod/responseReasoningSummaryPartAddedStreamingEventSchema";
import { responseReasoningSummaryPartDoneStreamingEventSchema } from "../generated/kubb/zod/responseReasoningSummaryPartDoneStreamingEventSchema";
import { responseRefusalDeltaStreamingEventSchema } from "../generated/kubb/zod/responseRefusalDeltaStreamingEventSchema";
import { responseRefusalDoneStreamingEventSchema } from "../generated/kubb/zod/responseRefusalDoneStreamingEventSchema";
import type { responseResourceSchema } from "../generated/kubb/zod/responseResourceSchema";
import { webSocketErrorEventSchema } from "../generated/kubb/zod/webSocketErrorEventSchema";

export const streamingEventSchema = z.union([
  responseCreatedStreamingEventSchema,
  responseQueuedStreamingEventSchema,
  responseInProgressStreamingEventSchema,
  responseCompletedStreamingEventSchema,
  responseFailedStreamingEventSchema,
  responseIncompleteStreamingEventSchema,
  responseOutputItemAddedStreamingEventSchema,
  responseOutputItemDoneStreamingEventSchema,
  responseContentPartAddedStreamingEventSchema,
  responseContentPartDoneStreamingEventSchema,
  responseOutputTextDeltaStreamingEventSchema,
  responseOutputTextDoneStreamingEventSchema,
  responseRefusalDeltaStreamingEventSchema,
  responseRefusalDoneStreamingEventSchema,
  responseFunctionCallArgumentsDeltaStreamingEventSchema,
  responseFunctionCallArgumentsDoneStreamingEventSchema,
  responseReasoningSummaryPartAddedStreamingEventSchema,
  responseReasoningSummaryPartDoneStreamingEventSchema,
  responseReasoningDeltaStreamingEventSchema,
  responseReasoningDoneStreamingEventSchema,
  responseReasoningSummaryDeltaStreamingEventSchema,
  responseReasoningSummaryDoneStreamingEventSchema,
  responseOutputTextAnnotationAddedStreamingEventSchema,
  errorStreamingEventSchema,
]);
export const webSocketStreamingEventSchema = z.union([
  streamingEventSchema,
  webSocketErrorEventSchema,
]);

export type StreamingEvent = z.infer<typeof streamingEventSchema>;
export type WebSocketStreamingEvent = z.infer<
  typeof webSocketStreamingEventSchema
>;

interface ParseStreamingEventOptions {
  transport?: "http" | "websocket";
}

export interface ParsedEvent {
  event: string;
  data: unknown;
  validationResult: z.SafeParseReturnType<
    unknown,
    StreamingEvent | WebSocketStreamingEvent
  >;
}

export interface SSEParseResult {
  events: ParsedEvent[];
  errors: string[];
  finalResponse: z.infer<typeof responseResourceSchema> | null;
}

const getEventType = (data: unknown) => {
  if (data && typeof data === "object" && "type" in data) {
    const type = (data as { type?: unknown }).type;
    if (typeof type === "string") return type;
  }
  return "unknown";
};

export function parseStreamingEventData(
  data: unknown,
  eventName?: string,
  options: ParseStreamingEventOptions = {},
): ParsedEvent {
  const validationSchema =
    options.transport === "websocket"
      ? webSocketStreamingEventSchema
      : streamingEventSchema;
  const validationResult = validationSchema.safeParse(data);
  return {
    event: eventName || getEventType(data),
    data,
    validationResult,
  };
}

export function getTerminalResponse(
  data: unknown,
): z.infer<typeof responseResourceSchema> | null {
  if (!data || typeof data !== "object") return null;

  const event = data as {
    type?: unknown;
    response?: z.infer<typeof responseResourceSchema>;
  };
  if (
    event.type === "response.completed" ||
    event.type === "response.failed" ||
    event.type === "response.incomplete"
  ) {
    return event.response ?? null;
  }

  return null;
}

export async function parseSSEStream(
  response: Response,
): Promise<SSEParseResult> {
  const events: ParsedEvent[] = [];
  const errors: string[] = [];
  let finalResponse: z.infer<typeof responseResourceSchema> | null = null;

  const reader = response.body?.getReader();
  if (!reader) {
    return { events, errors: ["No response body"], finalResponse };
  }

  const decoder = new TextDecoder();
  let buffer = "";

  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;

      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split("\n");
      buffer = lines.pop() || "";

      let currentEvent = "";
      let currentData = "";

      for (const line of lines) {
        if (line.startsWith("event:")) {
          currentEvent = line.slice(6).trim();
        } else if (line.startsWith("data:")) {
          currentData = line.slice(5).trim();
        } else if (line === "" && currentData) {
          if (currentData === "[DONE]") {
            // Skip the [DONE] sentinel - it's not a real event
          } else {
            try {
              const parsed = JSON.parse(currentData);
              const parsedEvent = parseStreamingEventData(parsed, currentEvent);
              events.push(parsedEvent);

              if (!parsedEvent.validationResult.success) {
                errors.push(
                  `Event validation failed for ${parsedEvent.event}: ${JSON.stringify(parsedEvent.validationResult.error.issues)}`,
                );
              }

              finalResponse = getTerminalResponse(parsed) ?? finalResponse;
            } catch {
              errors.push(`Failed to parse event data: ${currentData}`);
            }
          }
          currentEvent = "";
          currentData = "";
        }
      }
    }
  } finally {
    reader.releaseLock();
  }

  return { events, errors, finalResponse };
}
