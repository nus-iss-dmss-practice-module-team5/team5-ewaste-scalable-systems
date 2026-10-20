import axios from "axios";

export const api = axios.create({
  baseURL: "",
  timeout: 15_000,
  headers: {
    "Content-Type": "application/json",
  },
});

api.interceptors.request.use((config) => {
  const correlationId =
    globalThis.crypto?.randomUUID?.() ?? `corr-${Date.now()}`;
  config.headers["X-Correlation-ID"] = correlationId;
  return config;
});
