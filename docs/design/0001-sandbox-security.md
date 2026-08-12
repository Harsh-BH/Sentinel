# 0001 — Sandbox security model

**Status:** Accepted
**Implements:** `worker/internal/executor/sandbox.go`, `sandbox/nsjail/{python,cpp}.cfg`, `sandbox/policies/{python,cpp}.policy`

## Context

The worker runs untrusted code submitted by anonymous users. The threat surface is the worst case in any code-execution service: every submission is hostile until proven otherwise. We need defense in depth such that no single layer being weak or misconfigured allows escape.

The chosen primitive is [google/nsjail](https://github.com/google/nsjail) invoked as a subprocess from Go (`os/exec`), driven by a static protobuf config per language and a kafel seccomp policy. The worker writes source + stdin to an ephemeral `os.MkdirTemp` workdir, runs nsjail with that workdir as input, then deletes the workdir.

> **Current status:** all seven layers are active. The kafel seccomp policies are **enforced** (`seccomp_policy_file:` is set in both `sandbox/nsjail/*.cfg`) as allowlists with `DEFAULT KILL`.

## Decision

We design for seven independent isolation mechanisms. An attacker must break every active layer to reach the host. Six are currently active; layer 4 (seccomp) is authored but temporarily disabled — see the note above and [Known limitations](#known-limitations):

1. **`pivot_root` to a minimal rootfs** — only the language runtime (`python3.12` or `g++`) plus required `.so` files are visible. No `/etc/passwd`, no `/proc/self/exe` resolves to anything useful, no host paths reachable.
2. **All seven Linux namespaces** — `PID`, `NET`, `MNT`, `UTS`, `IPC`, `USER`, `CGROUP`. Empty network namespace means even a kernel bug that unblocks `socket(2)` produces an unreachable socket — there is no route, no DNS, no `lo`.
3. **Cgroups v2 limits** — `memory.max`, `pids.max`, `cpu.max`. Enforced by the kernel, not the runtime; even a runtime escape doesn't bypass them.
4. **Seccomp-BPF allowlist via kafel** — `sandbox/policies/{python,cpp}.policy`, `DEFAULT KILL` for anything unlisted. Because it is an allowlist, dangerous syscalls are blocked by omission rather than by enumeration: `ptrace`, `process_vm_readv/writev`, `mount`/`umount2`/`pivot_root`/`chroot`/`unshare`/`setns`, `bpf`, `perf_event_open`, `io_uring_setup`, `userfaultfd`, `init_module`/`kexec_load`, `setuid`/`setgid`/`capset`, `clock_settime`, `keyctl`, and the whole `socket`/`connect`/`bind`/`sendto` family are all unreachable. The socket denial overlaps with the empty network namespace (layer 2) on purpose — two independent mechanisms, so a regression in one does not open the network.
5. **Container-level hardening** — worker pod runs as non-root, drops all capabilities, mounts `/` read-only, with `securityContext.readOnlyRootFilesystem: true`.
6. **NetworkPolicy default-deny** in the `sentinel` namespace — even if a sandboxed process somehow obtained network access, kube-proxy/CNI rules would drop the packets.
7. **Application-layer guards** — request body limit (1 MB), source code limit (1 MB enforced in usecase), per-IP rate limiting, language allowlist.

### Output capture

stdout and stderr are read through `bytes.Buffer` wrapped to cap at 64 KB each (`maxOutputBytes` in `sandbox.go`); excess is truncated and replaced with a marker. This prevents a malicious program from exhausting worker memory by emitting unbounded output.

### Wall-clock enforcement

Two redundant timers:
- nsjail's `time_limit` (kernel-enforced, kills the cgroup)
- A Go `context.WithTimeout` around `cmd.Run()` plus `Setpgid` so we can `kill(-pgid, SIGKILL)` even if nsjail itself hangs.

Both have to fail for a job to escape its time budget.

### C++ two-pass

C++ submissions go through two separate sandboxed invocations: a compile pass (g++ with stricter limits — 512 MB memory, no network, allowlisted compile syscalls) followed by an execute pass with the standard runtime policy. Compilation artifacts are written to the same ephemeral workdir, which is destroyed when the job ends.

## Alternatives considered

- **gVisor / runsc** — true userspace kernel, blocks the entire host syscall surface. Rejected because (a) gVisor's syscall implementation is incomplete; some Python C-extensions misbehave, (b) overhead of ~30% on syscall-heavy workloads, (c) operating gVisor adds another runtime to debug. We may revisit if we add languages with richer syscall use.
- **Firecracker microVMs** — strongest isolation (KVM hardware boundary), used by AWS Lambda. Rejected for now because cold-start is 100–300 ms even with snapshot restore; our target p99 is sub-second total including queue + DB. Reconsider if we ever offer paid tiers with stickier sandboxes.
- **Docker / containerd as the only boundary** — rejected. Container escape CVEs appear regularly; `runc` shares a kernel with the host with only namespaces and seccomp between them. nsjail gives us the same primitives but with fine-grained, per-language seccomp policies that container runtimes don't make easy to author.
- **Bubblewrap** — viable, similar primitives. nsjail was chosen because its protobuf config is more declarative than bwrap's CLI args, the kafel policy DSL is purpose-built for seccomp authorship, and Google has a strong record maintaining it.

## Verification

`scripts/security-audit.sh` runs a battery of known-malicious payloads against a live worker:
- Filesystem escape attempts (path traversal, `/proc` snooping)
- Network exfiltration (DNS, raw socket, HTTP)
- Privilege escalation (`setuid`, `mount`, kernel module load)
- Resource exhaustion (fork bomb, allocator bomb, infinite loop)
- ptrace-based escape

CI runs the audit on every push to main.

## Host requirements

The sandbox cannot run on a non-Linux host. Specifically:

- **Linux kernel ≥ 5.4** with **cgroup v2** mounted at `/sys/fs/cgroup` (single hierarchy). All current Debian/Ubuntu/Fedora/Arch releases satisfy this. nsjail's cgroup v2 mode requires it (see `use_cgroupv2: true` in `sandbox/nsjail/*.cfg`); cgroup v1 is rejected.
- **`CAP_SYS_ADMIN`** at the worker process level (the worker container runs `privileged: true` in compose; in production the Pod has `securityContext.capabilities.add: [SYS_ADMIN]`). Required for namespace creation and cgroup hierarchy manipulation.
- **`cgroupns: host`** when running under Docker. nsjail's cgroup v2 setup writes to `cgroup.subtree_control` on the host hierarchy; with the default `cgroupns: private` Docker hides that hierarchy and nsjail cannot proceed.
- **macOS / Windows** can host the *control plane* (API, frontend, deps) via Docker Desktop, but the worker only functions when its container actually runs on a Linux kernel — i.e. Docker Desktop's WSL2 backend on Windows, or a Linux VM on macOS. Native macOS Docker (xhyve/HyperKit) does not provide a usable cgroup v2 hierarchy.

## Known limitations

- **~~Seccomp policy temporarily disabled.~~ RESOLVED.** The policies now load and are enforced.

  Worth recording how this was diagnosed, because the original note misread the problem as "kafel's table differs from libc's, so audit the names". The real situation was narrower and more interesting: of ~110 candidate syscalls, kafel rejected exactly **two** — `futex_time64` (a 32-bit-only variant, irrelevant on x86_64) and `uname` (kafel spells it `newuname`, the kernel's internal name). `fstat`, the name in the original error message, was rejected *and* unnecessary: current glibc issues `newfstatat`, so CPython never calls `fstat` at all. The blocker was one dead entry in a hand-written list.

  The list is now derived rather than authored: `strace -f` on CPython (stdin, file I/O, threading, json, random, math, time, subprocess) and on `g++ -O2` (which fork/execs cc1plus, as and ld), unioned with a curated margin for signal and event-loop paths that tracing did not exercise. `restart_syscall` is the subtle one — omit it and any program interrupted by a signal mid-syscall dies.

  Regression coverage lives in `scripts/integration-test.sh`: a realistic Python program must still succeed, while `socket()` and `ptrace()` must be killed.
- **Side-channels.** We do not defend against timing or cache side-channels between concurrent sandboxes on the same node. This is out of scope: we don't process secrets in the sandbox.
- **Host kernel CVEs.** A new local-privilege-escalation in the kernel is, by definition, unmitigated. The seccomp allowlist (when re-enabled) narrows the attack surface but does not eliminate it. We accept this risk and rely on host patching cadence.
- **Resource accounting precision.** `memory_used_kb` is meant to be read from cgroup `memory.peak`. On the current cgroup v2 / cgroupns:host setup that read is failing silently — every job reports `memory_used_kb: 0`. The execution itself is unaffected; only the metric is wrong. *Action item: pull peak from the worker's own cgroup tree rather than the ephemeral workdir.*
