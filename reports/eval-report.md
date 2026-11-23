# goplay evaluation report

- **Schema**: eval-v1
- **Mode**: quick
- **Generated**: 2026-11-23T20:09:03+08:00
- **Duration**: 11570 ms
- **Environment**: windows/amd64, Go go1.27.4, 16 CPUs

## Verdict: PASS

suites: 6 passed / 0 failed; threshold checks: 47 passed / 0 failed

## Threshold table

| # | Suite | Metric | Value | Rule | Pass |
|---|-------|--------|-------|------|------|
| 1 | perception | overall field accuracy on clean frames | 1.0000 | >= 0.9900 | PASS |
| 2 | perception | overall field accuracy at 0.1% pixel noise | 0.9932 | >= 0.9700 | PASS |
| 3 | perception | overall field accuracy at 0.5% pixel noise | 0.9805 | >= 0.9000 | PASS |
| 4 | perception | overall field accuracy at 1% pixel noise | 0.9552 | >= 0.8500 | PASS |
| 5 | perception | HP accuracy at 1% pixel noise | 0.9990 | >= 0.9000 | PASS |
| 6 | perception | enemy HP accuracy at 1% pixel noise | 0.9823 | >= 0.9500 | PASS |
| 7 | perception | room accuracy at 1% pixel noise | 0.9802 | >= 0.9500 | PASS |
| 8 | perception | potion-count accuracy at 1% pixel noise | 0.8594 | >= 0.8000 | PASS |
| 9 | learning | win rate over all episodes | 1.0000 | >= 0.5000 | PASS |
| 10 | learning | fraction of seeds reaching 90% progress | 1.0000 | == 1.0000 | PASS |
| 11 | learning | average attempts to reach 90% progress | 1 | <= 4 | PASS |
| 12 | learning | engine errors | 0 | == 0 | PASS |
| 13 | memory | memory stays within the 256KB budget | 262078 | <= 262144 | PASS |
| 14 | memory | at least one compaction happened | 446 | >= 1 | PASS |
| 15 | memory | at least one low-value entry was evicted | 446 | >= 1 | PASS |
| 16 | memory | must-remember Recall@1 after compaction | 1.0000 | >= 0.9000 | PASS |
| 17 | memory | must-remember Recall@3 after compaction | 1.0000 | >= 0.9500 | PASS |
| 18 | memory | must-remember Recall@5 after compaction | 1.0000 | >= 0.9500 | PASS |
| 19 | memory | working events were folded into summaries | 3 | >= 1 | PASS |
| 20 | memory | at least 8 summaries retained the must keys | 12 | >= 8 | PASS |
| 21 | memory | must-remember event Recall@1 after compression | 1.0000 | >= 0.9000 | PASS |
| 22 | memory | must-remember event Recall@3 after compression | 1.0000 | >= 0.9500 | PASS |
| 23 | memory | must-remember event Recall@5 after compression | 1.0000 | >= 0.9500 | PASS |
| 24 | control | human takeover applied within 1s | 9.00 ms | <= 1000.00 ms | PASS |
| 25 | control | no actions execute while the human is in control | 1 | == 1 | PASS |
| 26 | control | recovery within 2s of release | 132.00 ms | <= 2000.00 ms | PASS |
| 27 | control | engine recorded a handover latency below 1s | 9.00 ms | <= 1000.00 ms | PASS |
| 28 | control | constraint applied within 1s | 9.00 ms | <= 1000.00 ms | PASS |
| 29 | control | no attack after the constraint boundary | 0 | == 0 | PASS |
| 30 | control | play continues after constraint | 5 | >= 3 | PASS |
| 31 | control | preference applied within 1s | 11.00 ms | <= 1000.00 ms | PASS |
| 32 | control | risk_aversion weight is at least 0.2 | 0.2500 | >= 0.2000 | PASS |
| 33 | control | play continues after preference | 1 | == 1 | PASS |
| 34 | concurrency | 2 instances all running before takeover | 2 | == 2 | PASS |
| 35 | concurrency | 2 instances: no cross-talk on human takeover | 0 | == 0 | PASS |
| 36 | concurrency | 2 instances: no shared game/memory/ID | 0 | == 0 | PASS |
| 37 | concurrency | 2 instances: every instance keeps acting | 6 | >= 5 | PASS |
| 38 | concurrency | 4 instances all running before takeover | 4 | == 4 | PASS |
| 39 | concurrency | 4 instances: no cross-talk on human takeover | 0 | == 0 | PASS |
| 40 | concurrency | 4 instances: no shared game/memory/ID | 0 | == 0 | PASS |
| 41 | concurrency | 4 instances: every instance keeps acting | 6 | >= 5 | PASS |
| 42 | localmodel | first token under 500ms | 10.00 ms | <= 500.00 ms | PASS |
| 43 | localmodel | decode speed at least 20 tokens/sec | 106.8 | >= 20.0 | PASS |
| 44 | localmodel | all 8 stream deltas parsed | 8 | == 8 | PASS |
| 45 | localmodel | streamed text matches the mock | 1 | == 1 | PASS |
| 46 | localmodel | TTFT histogram and TPS gauge are wired | 1 | == 1 | PASS |
| 47 | localmodel | llm.Mock completes offline | 1 | == 1 | PASS |

## Perception accuracy under renderer noise (pass, 914 ms)

| Metric | Value | Threshold |
|--------|-------|-----------|
| HP accuracy @ level 0 | 1.0000 | - |
| enemy HP accuracy @ level 0 | 1.0000 | - |
| room accuracy @ level 0 | 1.0000 | - |
| potions accuracy @ level 0 | 1.0000 | - |
| HP accuracy @ level 1 | 1.0000 | - |
| enemy HP accuracy @ level 1 | 0.9979 | - |
| room accuracy @ level 1 | 0.9969 | - |
| potions accuracy @ level 1 | 0.9781 | - |
| HP accuracy @ level 2 | 1.0000 | - |
| enemy HP accuracy @ level 2 | 0.9990 | - |
| room accuracy @ level 2 | 0.9958 | - |
| potions accuracy @ level 2 | 0.9271 | - |
| HP accuracy @ level 3 | 0.9990 | - |
| enemy HP accuracy @ level 3 | 0.9823 | - |
| room accuracy @ level 3 | 0.9802 | - |
| potions accuracy @ level 3 | 0.8594 | - |
| overall field accuracy on clean frames | 1.0000 | >= 0.9900 |
| overall field accuracy at 0.1% pixel noise | 0.9932 | >= 0.9700 |
| overall field accuracy at 0.5% pixel noise | 0.9805 | >= 0.9000 |
| overall field accuracy at 1% pixel noise | 0.9552 | >= 0.8500 |
| HP accuracy at 1% pixel noise | 0.9990 | >= 0.9000 |
| enemy HP accuracy at 1% pixel noise | 0.9823 | >= 0.9500 |
| room accuracy at 1% pixel noise | 0.9802 | >= 0.9500 |
| potion-count accuracy at 1% pixel noise | 0.8594 | >= 0.8000 |

- 8 seeds x 120 steps x 4 noise levels; HP tolerance 1.0, enemy HP tolerance 1.0, room/potions exact.
- GroundTruth is mockgame.GroundTruth(); the analyzer never reads it.

<details><summary>Raw perception details</summary>

```json
{
  "levels": [
    {
      "noise": 0,
      "counts": {
        "samples": 960,
        "hp_correct": 960,
        "enemy_hp_correct": 960,
        "room_correct": 960,
        "potions_correct": 960,
        "dead_correct": 960,
        "won_correct": 952
      },
      "hp_accuracy": 1,
      "enemy_hp_accuracy": 1,
      "room_accuracy": 1,
      "potions_accuracy": 1,
      "overall_accuracy": 1
    },
    {
      "noise": 0.001,
      "counts": {
        "samples": 960,
        "hp_correct": 960,
        "enemy_hp_correct": 958,
        "room_correct": 957,
        "potions_correct": 939,
        "dead_correct": 960,
        "won_correct": 952
      },
      "hp_accuracy": 1,
      "enemy_hp_accuracy": 0.9979166666666667,
      "room_accuracy": 0.996875,
      "potions_accuracy": 0.978125,
      "overall_accuracy": 0.9932291666666667
    },
    {
      "noise": 0.005,
      "counts": {
        "samples": 960,
        "hp_correct": 960,
        "enemy_hp_correct": 959,
        "room_correct": 956,
        "potions_correct": 890,
        "dead_correct": 960,
        "won_correct": 952
      },
      "hp_accuracy": 1,
      "enemy_hp_accuracy": 0.9989583333333333,
      "room_accuracy": 0.9958333333333333,
      "potions_accuracy": 0.9270833333333334,
      "overall_accuracy": 0.98046875
    },
    {
      "noise": 0.01,
      "counts": {
        "samples": 960,
        "hp_correct": 959,
        "enemy_hp_correct": 943,
        "room_correct": 941,
        "potions_correct": 825,
        "dead_correct": 960,
        "won_correct": 952
      },
      "hp_accuracy": 0.9989583333333333,
      "enemy_hp_accuracy": 0.9822916666666667,
      "room_accuracy": 0.9802083333333333,
      "potions_accuracy": 0.859375,
      "overall_accuracy": 0.9552083333333333
    }
  ],
  "seeds": [
    3,
    7,
    11,
    19,
    23,
    29,
    31,
    37
  ],
  "steps": 120
}
```

</details>

## Learning convergence across episodes (pass, 8967 ms)

| Metric | Value | Threshold |
|--------|-------|-----------|
| episodes completed across seeds | 24 | - |
| actions executed across seeds | 1782 | - |
| win rate over all episodes | 1.0000 | >= 0.5000 |
| fraction of seeds reaching 90% progress | 1.0000 | == 1.0000 |
| average attempts to reach 90% progress | 1 | <= 4 |
| engine errors | 0 | == 0 |

- Each seed reuses one Policy and one Memory instance for all episodes; Reset() reloads a fresh mock game.
- Learning signal is the action reward computed from the built-in mock_dungeon profile plus UCB exploration.

<details><summary>Raw learning details</summary>

```json
{
  "configured_episodes": 8,
  "seeds": [
    {
      "seed": 7,
      "status": "stopped",
      "episodes": 8,
      "wins": 8,
      "win_rate": 1,
      "reached_90": true,
      "attempts_to_90": 1,
      "best_progress": 0.999,
      "actions": 589,
      "errors": 0,
      "episodes_detail": [
        {
          "index": 1,
          "won": true,
          "progress": 0.999,
          "actions": 76,
          "reward": 164.46666666666667
        },
        {
          "index": 2,
          "won": true,
          "progress": 0.999,
          "actions": 76,
          "reward": 164.89999999999972
        },
        {
          "index": 3,
          "won": true,
          "progress": 0.999,
          "actions": 83,
          "reward": 164.59999999999974
        },
        {
          "index": 4,
          "won": true,
          "progress": 0.999,
          "actions": 73,
          "reward": 164.516666666667
        },
        {
          "index": 5,
          "won": true,
          "progress": 0.999,
          "actions": 71,
          "reward": 164.16666666666708
        },
        {
          "index": 6,
          "won": true,
          "progress": 0.999,
          "actions": 73,
          "reward": 164.2000000000004
        },
        {
          "index": 7,
          "won": true,
          "progress": 0.999,
          "actions": 71,
          "reward": 164.6166666666668
        },
        {
          "index": 8,
          "won": true,
          "progress": 0.999,
          "actions": 66,
          "reward": 175.08333333333417
        }
      ]
    },
    {
      "seed": 11,
      "status": "stopped",
      "episodes": 8,
      "wins": 8,
      "win_rate": 1,
      "reached_90": true,
      "attempts_to_90": 1,
      "best_progress": 0.999,
      "actions": 596,
      "errors": 0,
      "episodes_detail": [
        {
          "index": 1,
          "won": true,
          "progress": 0.999,
          "actions": 79,
          "reward": 164.60000000000005
        },
        {
          "index": 2,
          "won": true,
          "progress": 0.999,
          "actions": 78,
          "reward": 164.24999999999986
        },
        {
          "index": 3,
          "won": true,
          "progress": 0.999,
          "actions": 74,
          "reward": 164.16666666666652
        },
        {
          "index": 4,
          "won": true,
          "progress": 0.999,
          "actions": 75,
          "reward": 164.41666666666697
        },
        {
          "index": 5,
          "won": true,
          "progress": 0.999,
          "actions": 74,
          "reward": 164.3166666666673
        },
        {
          "index": 6,
          "won": true,
          "progress": 0.999,
          "actions": 70,
          "reward": 164.39999999999986
        },
        {
          "index": 7,
          "won": true,
          "progress": 0.999,
          "actions": 71,
          "reward": 164.3166666666666
        },
        {
          "index": 8,
          "won": true,
          "progress": 0.999,
          "actions": 75,
          "reward": 164.73333333333244
        }
      ]
    },
    {
      "seed": 23,
      "status": "stopped",
      "episodes": 8,
      "wins": 8,
      "win_rate": 1,
      "reached_90": true,
      "attempts_to_90": 1,
      "best_progress": 0.999,
      "actions": 597,
      "errors": 0,
      "episodes_detail": [
        {
          "index": 1,
          "won": true,
          "progress": 0.999,
          "actions": 78,
          "reward": 164.2666666666667
        },
        {
          "index": 2,
          "won": true,
          "progress": 0.999,
          "actions": 75,
          "reward": 164.54999999999978
        },
        {
          "index": 3,
          "won": true,
          "progress": 0.999,
          "actions": 81,
          "reward": 164.56249999999983
        },
        {
          "index": 4,
          "won": true,
          "progress": 0.999,
          "actions": 73,
          "reward": 163.9833333333333
        },
        {
          "index": 5,
          "won": true,
          "progress": 0.999,
          "actions": 74,
          "reward": 164.13333333333355
        },
        {
          "index": 6,
          "won": true,
          "progress": 0.999,
          "actions": 77,
          "reward": 164.55000000000052
        },
        {
          "index": 7,
          "won": true,
          "progress": 0.999,
          "actions": 71,
          "reward": 164.08333333333314
        },
        {
          "index": 8,
          "won": true,
          "progress": 0.999,
          "actions": 68,
          "reward": 164.44999999999982
        }
      ]
    }
  ]
}
```

</details>

## Memory recall under a 256KB budget (pass, 411 ms)

| Metric | Value | Threshold |
|--------|-------|-----------|
| fixed memory budget | 262144 | - |
| bytes used after noise (scenario A) | 262078 | - |
| compactions (scenario A) | 446 | - |
| evictions (scenario A) | 446 | - |
| summaries (scenario A) | 0 | - |
| Recall@1 on must-remember facts | 1.0000 | - |
| Recall@3 on must-remember facts | 1.0000 | - |
| Recall@5 on must-remember facts | 1.0000 | - |
| memory stays within the 256KB budget | 262078 | <= 262144 |
| at least one compaction happened | 446 | >= 1 |
| at least one low-value entry was evicted | 446 | >= 1 |
| must-remember Recall@1 after compaction | 1.0000 | >= 0.9000 |
| must-remember Recall@3 after compaction | 1.0000 | >= 0.9500 |
| must-remember Recall@5 after compaction | 1.0000 | >= 0.9500 |
| summaries produced (scenario B) | 12 | - |
| working entries remaining (scenario B) | 32 | - |
| Recall@1 on must-remember events | 1.0000 | - |
| Recall@3 on must-remember events | 1.0000 | - |
| Recall@5 on must-remember events | 1.0000 | - |
| working events were folded into summaries | 3 | >= 1 |
| at least 8 summaries retained the must keys | 12 | >= 8 |
| must-remember event Recall@1 after compression | 1.0000 | >= 0.9000 |
| must-remember event Recall@3 after compression | 1.0000 | >= 0.9500 |
| must-remember event Recall@5 after compression | 1.0000 | >= 0.9500 |

- Recall is counted as hit only when the correct labeled entry appears in the top K and its text contains the unique token.
- Must-remember facts use importance 0.95, which the memory package protects from eviction.

<details><summary>Raw memory details</summary>

```json
{
  "budget_kb": 256,
  "eviction": {
    "fillers": 900,
    "must_entries": 40,
    "noise_events": 240,
    "recall_at_1": {
      "k": 1,
      "hits": 40,
      "total": 40,
      "rate": 1
    },
    "recall_at_3": {
      "k": 3,
      "hits": 40,
      "total": 40,
      "rate": 1
    },
    "recall_at_5": {
      "k": 5,
      "hits": 40,
      "total": 40,
      "rate": 1
    },
    "stats": {
      "added": 1180,
      "bytes": 262078,
      "compactions": 446,
      "evictions": 446,
      "facts": 732,
      "recalls": 0,
      "summaries": 0,
      "working": 2
    }
  },
  "summarization": {
    "events": 128,
    "must_entries": 16,
    "recall_at_1": {
      "k": 1,
      "hits": 16,
      "total": 16,
      "rate": 1
    },
    "recall_at_3": {
      "k": 3,
      "hits": 16,
      "total": 16,
      "rate": 1
    },
    "recall_at_5": {
      "k": 5,
      "hits": 16,
      "total": 16,
      "rate": 1
    },
    "stats": {
      "added": 128,
      "bytes": 6244,
      "compactions": 3,
      "evictions": 0,
      "facts": 0,
      "recalls": 0,
      "summaries": 12,
      "working": 32
    }
  }
}
```

</details>

## Human takeover and intervention boundaries (pass, 434 ms)

| Metric | Value | Threshold |
|--------|-------|-----------|
| Touch -> human_control latency | 9.00 ms | - |
| Actions frozen during human control (1 = yes) | 1 | - |
| Release -> running and acting | 132.00 ms | - |
| engine handover_latency_ms average | 9.00 ms | - |
| HumanTakeovers counter | 1 | - |
| Submit constraint -> applied to Control | 9.00 ms | - |
| attack actions after constraint (grace: first action) | 0 | - |
| actions executed after constraint | 5 | - |
| Submit preference -> risk_aversion weight | 11.00 ms | - |
| risk_aversion weight after preference | 0.2500 | - |
| actions still advance after preference (1 = yes) | 1 | - |
| human takeover applied within 1s | 9.00 ms | <= 1000.00 ms |
| no actions execute while the human is in control | 1 | == 1 |
| recovery within 2s of release | 132.00 ms | <= 2000.00 ms |
| engine recorded a handover latency below 1s | 9.00 ms | <= 1000.00 ms |
| constraint applied within 1s | 9.00 ms | <= 1000.00 ms |
| no attack after the constraint boundary | 0 | == 0 |
| play continues after constraint | 5 | >= 3 |
| preference applied within 1s | 11.00 ms | <= 1000.00 ms |
| risk_aversion weight is at least 0.2 | 0.2500 | >= 0.2000 |
| play continues after preference | 1 | == 1 |

- HumanResumeMS is set to 120ms so the suite runs quickly; production defaults to 2500ms.
- MockHuman is active for 1500ms after Touch; the suite calls Release explicitly.
- The first action after the observed boundary may have been in flight and is excluded from the constraint check.

<details><summary>Raw control details</summary>

```json
{
  "boundary": {
    "adaptations": [
      {
        "preference_id": "p721f1904455820f",
        "preference": "你太激进了 | hint: play less aggressively",
        "outcome": "applied",
        "reason": "converted to soft weights",
        "at": "2026-11-23T20:09:13.774493+08:00"
      }
    ],
    "constraints": 1,
    "hints": [
      "你太激进了 | hint: play less aggressively"
    ],
    "intervention_latency_ms": 9,
    "play_continues": true,
    "post_constraint_actions": 5,
    "post_constraint_attacks": 0,
    "preference_latency_ms": 11,
    "preference_weight": 0.25
  },
  "takeover": {
    "actions_after_freeze": 5,
    "actions_before_freeze": 5,
    "handover_avg_ms": 9,
    "human_takeovers": 1,
    "recovery_latency_ms": 132,
    "takeover_latency_ms": 9
  }
}
```

</details>

## Concurrent instance isolation (pass, 708 ms)

| Metric | Value | Threshold |
|--------|-------|-----------|
| 2 instances: minimum actions executed | 6 | - |
| 2 instances all running before takeover | 2 | == 2 |
| 2 instances: no cross-talk on human takeover | 0 | == 0 |
| 2 instances: no shared game/memory/ID | 0 | == 0 |
| 2 instances: every instance keeps acting | 6 | >= 5 |
| 4 instances: minimum actions executed | 6 | - |
| 4 instances all running before takeover | 4 | == 4 |
| 4 instances: no cross-talk on human takeover | 0 | == 0 |
| 4 instances: no shared game/memory/ID | 0 | == 0 |
| 4 instances: every instance keeps acting | 6 | >= 5 |

- Each instance uses its own memory directory, Lua state, mockgame.Game, Metrics registry and Policy.
- Cross-talk is tested by touching only instance 0 and confirming every other instance still advances actions.

<details><summary>Raw concurrency details</summary>

```json
{
  "sizes": [
    {
      "instances": 2,
      "all_active": 2,
      "min_actions": 6,
      "action_counts": [
        6,
        34
      ],
      "progress": [
        0.15000000000000002,
        0.5441666666666667
      ],
      "crosstalk_violations": 0,
      "isolation_violations": 0
    },
    {
      "instances": 4,
      "all_active": 4,
      "min_actions": 6,
      "action_counts": [
        6,
        34,
        34,
        34
      ],
      "progress": [
        0.15000000000000002,
        0.5441666666666667,
        0.5333333333333333,
        0.6266666666666666
      ],
      "crosstalk_violations": 0,
      "isolation_violations": 0
    }
  ]
}
```

</details>

## Local model TTFT/TPS metric chain (pass, 132 ms)

| Metric | Value | Threshold |
|--------|-------|-----------|
| SSE first-token latency | 10.00 ms | - |
| SSE full stream latency | 85.00 ms | - |
| streamed completion tokens | 8 | - |
| decode tokens per second after first token | 106.8 | - |
| LocalTTFTMS histogram samples | 1 | - |
| LocalTPS gauge value | 106.8 | - |
| llm.Mock latency | 15.00 ms | - |
| preflight request including first loopback connect | 2.00 ms | - |
| first token under 500ms | 10.00 ms | <= 500.00 ms |
| decode speed at least 20 tokens/sec | 106.8 | >= 20.0 |
| all 8 stream deltas parsed | 8 | == 8 |
| streamed text matches the mock | 1 | == 1 |
| TTFT histogram and TPS gauge are wired | 1 | == 1 |
| llm.Mock completes offline | 1 | == 1 |

- The SSE server is local httptest; no external network or real model is required.
- TTFT is measured by the production llm.OpenAI.CompleteStream parser, not by the test harness.
- A non-stream preflight request absorbs host-specific first-connect latency; its cost is reported separately as warmup_ms and is not counted as model TTFT.

<details><summary>Raw localmodel details</summary>

```json
{
  "auth_header_seen": true,
  "completion_tokens": 8,
  "got_text": "alpha beta gamma delta alpha beta gamma delta ",
  "mock_latency_ms": 15,
  "mock_text": "{\"kind\":\"attack\",\"reason\":\"mock\"}",
  "stream_latency_ms": 85,
  "tps": 106.77101754114355,
  "ttft_ms": 10,
  "want_text": "alpha beta gamma delta alpha beta gamma delta ",
  "warmup_ms": 2
}
```

</details>

## Limitations

- Perception is measured on the built-in 320x180 mock renderer, not on a real game window or GPU capture.
- Learning convergence uses the built-in mock_dungeon Lua profile and mock game; absolute numbers are not transferable to a commercial game.
- Memory scenarios use synthetic entries with unique tokens; Recall@K is exact-token presence, not semantic judgment.
- Local-model TTFT/TPS is proven through a local httptest SSE endpoint and llm.Mock; no real local LLM is required or launched.
- Timing thresholds are generous and assume an idle host; CI under load can miss latency targets even when behavior is correct.
- Safety auditing is a separate package (internal/audit); eval does not replace it.
- All tests are offline and do not inject into, read memory of, or hook any real game process.
