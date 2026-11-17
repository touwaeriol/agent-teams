// Voice input API: the composer's config and client secrets, the admin's 语音 settings.
import { useQuery } from "@tanstack/vue-query";
import type { VoiceSessionInfo } from "@/lib/voice/openai";
import { client, unwrap } from "./client";
import type { components } from "./schema";

type S = components["schemas"];
export type VoiceAdmin = S["VoiceAdmin"];
export type VoiceAdminInput = S["VoiceAdminInput"];
export type VoiceConfig = S["VoiceConfig"];

export const voiceKeys = {
  config: ["voice", "config"] as const,
  admin: ["admin", "voice"] as const,
};

/** Whether the mic button shows (admin enabled voice with a usable key) and the languages. */
export const useVoiceConfig = () =>
  useQuery({ queryKey: voiceKeys.config, queryFn: () => unwrap(client.GET("/api/voice/config")), staleTime: 5 * 60_000, retry: false });

export const useVoiceAdmin = () => useQuery({ queryKey: voiceKeys.admin, queryFn: () => unwrap(client.GET("/api/admin/voice")) });

export const voice = {
  session: async (language: string): Promise<VoiceSessionInfo> =>
    (await unwrap(client.POST("/api/voice/session", { body: { language }, signal: AbortSignal.timeout(10_000) }))) as VoiceSessionInfo,
  save: (body: VoiceAdminInput) => unwrap(client.PUT("/api/admin/voice", { body })),
  test: (body: { api_key?: string; model?: string; base_url?: string }) => unwrap(client.POST("/api/admin/voice/test", { body })),
};
