// Package localmodel selects, downloads, serves and benchmarks small local
// GGUF models for the goplay agent.
//
// Design:
//   - Tiers (low/medium/high) are chosen from physical RAM and, optionally, an
//     explicit preference or an Apple Silicon efficiency bump.
//   - Roles are split by job: perception uses rules first and a tiny model only
//     for ambiguity, strategy uses one reasoning-capable model, and validator is
//     always deterministic rules with final authority. Roles are sequential, not
//     an ensemble and not "more models = more capability".
//   - Model weights are never embedded in the installer. The first run resolves
//     a tier and downloads only the models required by its role plan.
//   - Downloads use resumable HTTP ranges, retries, SHA256 verification and an
//     atomic temporary-file rename. Model bytes can come from Hugging Face or a
//     mirror configured with GOPLAY_HF_ENDPOINT.
//   - Serving uses an external llama.cpp llama-server child process bound to
//     127.0.0.1. No llama.cpp binary is bundled; RuntimePath/GOPLAY_LLAMA_SERVER
//     must point at it.
//
// Security boundary: this package never injects into a game process, never reads
// or writes game memory, never hooks graphics APIs and never loads a kernel
// driver. It only performs outbound HTTPS downloads and talks HTTP to a loopback
// llama-server.
package localmodel
