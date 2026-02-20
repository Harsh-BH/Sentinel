Project Sentinel: Architectural Blueprint for a High-Concurrency Distributed Remote Code Execution Engine
1. Introduction and Architectural Imperatives
The engineering of a high-concurrency, distributed remote code execution (RCE) engine demands an architecture capable of reconciling contradictory operational requirements. The system must ingest and execute untrusted, potentially hostile arbitrary payloads in languages such as C++ and Python, return deterministic outputs, and enforce absolute isolation, all while sustaining throughputs exceeding 10,000 executions per minute.1 Project Sentinel represents a production-grade system designed to emulate and exceed the backend architectures of tier-one competitive programming platforms and technical screening environments.
To achieve this scale, the system architecture must wholly abandon synchronous, blocking execution models. Synchronous HTTP requests that hold open connections while waiting for compilation and execution to finish inevitably lead to thread pool exhaustion, cascading timeouts, and catastrophic failure under load spikes.2 Instead, Project Sentinel leverages a decoupled, event-driven topology. The ingestion layer immediately acknowledges receipt of the payload, offloading the computationally expensive execution phases to an asynchronously scaled pool of isolated worker processes.4 This report exhaustively details the integration of the Go programming language, the RabbitMQ message broker, PostgreSQL state management, Kubernetes orchestration, and the Linux kernel's nsjail isolation primitives to construct a fault-tolerant, scalable, and highly secure RCE engine.
2. Distributed Systems Architecture Design
The macroscopic architecture of Project Sentinel is predicated on the strict segregation of stateless ingestion mechanisms, durable message brokering, and stateful execution tracking. This separation of concerns ensures that no single component becomes a catastrophic bottleneck and allows for independent horizontal scaling.
2.1. API Gateway Design Patterns and Ingestion
The Ingestion Layer is constructed using Go and the Gin web framework, functioning as a purely stateless API gateway. When an end-user submits a payload, the Gin router parses the request and immediately generates a Universally Unique Identifier (UUIDv7). The selection of UUIDv7 is critical; unlike purely random UUIDv4 generation, UUIDv7 encodes a Unix timestamp within its leading bits, providing natural temporal locality. This temporal sortability drastically reduces index fragmentation and page faults when the identifier is subsequently used as a primary key within the PostgreSQL database.6
Upon validation of the payload metadata—such as language support and basic structural limits—the API gateway pushes the execution task statelessly to the message broker. Critically, the HTTP transaction terminates immediately following this push, returning a 202 Accepted status code alongside the generated UUID. This asynchronous response pattern informs the client that the request has been durably accepted for processing without holding the TCP connection hostage during the compilation and execution phases.2 The client application subsequently relies on polling the API with the UUID, or listening to a WebSocket connection, to retrieve the final execution status. Because the ingestion layer is entirely stateless, it sits behind an Nginx Ingress load balancer configured for simple round-robin or least-connections distribution, requiring no session affinity or sticky sessions.8
2.2. Message Queue Design Patterns and Broker Selection
The selection of the message broker dictates the backpressure mechanisms, fault tolerance, and routing capabilities of the entire distributed system. An architectural comparison between RabbitMQ, Apache Kafka, and NATS reveals distinct paradigms for handling task distribution.

Broker
Core Architecture Paradigm
Delivery and Routing Semantics
Suitability for Remote Code Execution
Apache Kafka
Distributed commit log, append-only immutable files.9
Pull-based, offsets tracked by consumer groups, optimized for stream processing.
Suboptimal. Kafka excels at massive data pipelines and event sourcing but lacks native per-message acknowledgment and fine-grained dead-letter routing necessary for individual job tracking.10
NATS
In-memory pub/sub (Core) or durable log-based streams (JetStream).9
At-most-once (Core) or At-least-once (JetStream), exceptionally high throughput.9
Suboptimal. While offering microsecond latency for "fire-and-forget" RPC, it lacks the complex queuing topologies and mature dead-letter exchange (DLX) features natively built into AMQP brokers.9
RabbitMQ
Advanced Message Queuing Protocol (AMQP 0-9-1) broker.9
Push-based with strict consumer prefetch limits, explicit individual acknowledgments.12
Optimal. Provides native task distribution, explicit message acknowledgments, sophisticated routing keys, and robust backpressure mechanisms critical for heterogeneous task execution.13

Project Sentinel utilizes RabbitMQ, specifically leveraging Quorum Queues. Quorum Queues replace traditional mirrored queues by implementing the Raft consensus algorithm, ensuring data safety and high availability across the Kubernetes cluster while avoiding the severe network partition vulnerabilities associated with older RabbitMQ synchronization methods.15
2.3. Push vs. Pull Worker Models and Backpressure
A fundamental design decision involves how workers receive tasks. A naive architecture might use a push model where the gateway directly fires HTTP requests at worker nodes. This approach lacks native backpressure; if workers are processing complex, long-running C++ compilations, an influx of requests will overwhelm the workers' internal buffers, leading to dropped tasks and CPU thrashing.16
Instead, Sentinel relies on RabbitMQ's consumer prefetch mechanism, which effectively transforms the AMQP push model into an intelligent pull model. By setting the Channel Prefetch Count to a very low value (e.g., prefetch=1), RabbitMQ is instructed to send only one unacknowledged message to a worker at a time.17 A worker engaged in a heavy compilation task will not be assigned further messages until it explicitly acknowledges the completion of the current task. Consequently, pending messages remain durably queued in RabbitMQ, where they can be dispatched to other idle workers, achieving perfect load distribution and inherent backpressure without complex application-level throttling.12
2.4. State Management: Redis vs. In-Memory vs. Database Tracking
While the API and workers are stateless, the lifecycle of a code execution task involves multiple state transitions (e.g., QUEUED, COMPILING, RUNNING, SUCCESS, RUNTIME_ERROR). Managing this state requires careful consideration of data stores.
Tracking state purely in-memory within the API nodes is impossible in a horizontally scaled environment due to the lack of shared memory. Redis provides microsecond latency for state tracking but lacks long-term durability; a total cluster failure could result in the loss of all execution histories. Therefore, Project Sentinel employs PostgreSQL (Supabase) as the absolute, durable source of truth for execution states.1 Redis is reserved strictly for transient operations: enforcing distributed locks for idempotency, caching frequently accessed algorithmic test cases to avoid database round-trips, and maintaining real-time rate limits for the API gateway.18
3. Real-World Reference Architectures
To guarantee production readiness, Sentinel's architecture incorporates design philosophies from established competitive programming platforms. Analyzing the evolutionary trajectories of Judge0, Codeforces, LeetCode, and DOMjudge provides critical insights into solving the trilemma of scale, security, and reliability.
3.1. Judge0
Judge0 operates on a highly decoupled client-server architecture designed explicitly for ease of deployment. It exposes a rich REST API that accepts code submissions and compilation flags.20 The architecture's primary strength lies in its isolation methodology; it relies heavily on robust sandboxing to restrict CPU time, memory, and standard I/O streams.20 Judge0 handles scalability by modularizing the API server from the background workers, allowing infrastructure administrators to scale the execution fleet independently of the web traffic handlers, a pattern Sentinel strictly emulates through its API-RabbitMQ-Worker topology.22
3.2. Codeforces
Codeforces requires handling massive bursts of traffic, particularly during the initial minutes of a scheduled contest where tens of thousands of users submit solutions concurrently.24 To manage this, Codeforces utilizes highly optimized, persistent queue structures and relies on C++ for its core backend services to minimize overhead. The architecture heavily leverages in-memory data grids to maintain real-time leaderboard updates, effectively decoupling the slow, durable storage from the fast, ephemeral contest state.18 Sentinel adopts this philosophy by utilizing Redis sorted sets to manage volatile contest states, preventing heavy database contention during concurrent leaderboard rendering.18
3.3. LeetCode
The LeetCode execution engine demonstrates the pinnacle of the long-running tasks pattern. Because executing user code against hundreds of hidden test cases can take several seconds, their backend immediately returns job identifiers while background worker pools process the payload asynchronously.2 LeetCode utilizes a sophisticated microservices architecture separated into problem fetching, evaluation, and contest management.24 Their execution environment leverages containerized pools to ensure that malicious code (such as fork bombs or unauthorized file access attempts) is strictly contained without crashing the host nodes.2 Sentinel replicates this asynchronous flow, ensuring the HTTP layer remains ultra-responsive regardless of backend execution depth.1
3.4. DOMjudge
DOMjudge, the standard automated system for the ICPC, is engineered with a focus on absolute reliability and strict jury control. It employs a highly secure "judgehost" worker architecture where the judging process is isolated from the web interface.25 DOMjudge is notable for its paranoid approach to execution environments, historically relying on heavily customized chroot jails and control groups to ensure that team submissions have absolutely zero access to network resources or arbitrary filesystems. This uncompromising security posture directly informs Sentinel's integration of advanced Linux namespace isolation.
4. Secure Sandboxing Architecture
The execution of untrusted, arbitrary code represents the single greatest security vulnerability in this system.1 Malicious actors will attempt to exploit the engine via resource starvation (memory exhaustion, fork bombs), system call exploitation (kernel panics, privilege escalation), and network reconnaissance (accessing internal cloud metadata endpoints).26 Mitigating these threats requires an evaluation of modern isolation technologies.
4.1. Comparative Analysis of Isolation Mechanisms

Technology
Isolation Paradigm
Boot Latency
Security Profile and Tradeoffs
Standard Docker
Linux Namespaces, Capabilities, default AppArmor/seccomp profiles.
Moderate (~500ms)
Low. Containers share the host kernel directly. Kernel zero-days allow for trivial container escapes. Unsuitable for deeply hostile multi-tenant workloads.27
gVisor
User-space kernel (Sentry) intercepting and servicing system calls.
Fast (~10-50ms)
High. Drastically reduces the host kernel attack surface. However, the system call interception introduces non-trivial performance overhead (10-30%) for I/O-heavy workloads.29
Firecracker
Hardware-virtualized MicroVMs leveraging KVM.
Moderate (~125ms)
Very High. Provides a dedicated, lightweight kernel per VM, enforcing strict hardware memory boundaries. Excellent for serverless, but the boot latency limits extreme high-throughput, short-lived script execution.29
Kata Containers
Full Virtual Machines orchestrated via standard container interfaces.
Slow (~150ms+)
Very High. Utilizes QEMU/KVM. Similar security profile to Firecracker but substantially heavier in memory footprint and startup time.28
nsjail
Raw Linux namespaces, cgroups v2, and kafel-compiled seccomp-bpf filters.
Instant (<5ms)
High. Provides stringent, cryptographically rigorous control over system calls and resources without the virtualization overhead. Employed by Google for highly sensitive workloads.31

Given the requirement to process over 10,000 executions per minute, where a typical Python script may only take 20 milliseconds to run, the 125ms boot latency of a MicroVM introduces unacceptable cumulative drag. Consequently, Project Sentinel standardizes on nsjail, which offers the near-zero latency of raw process execution alongside the robust security boundary of explicit kernel subsystem restrictions.29
4.2. Linux Kernel Internals and nsjail Configuration
Nsjail operates by directly manipulating low-level Linux kernel features. To safely run untrusted code, Sentinel configures nsjail to enforce strict boundaries across multiple vectors.
4.2.1. Filesystem Isolation: The Superiority of pivot_root over chroot
Historically, sandboxes relied on the chroot system call to restrict a process to a specific directory subtree. However, chroot is fundamentally flawed for security purposes. Because it only applies to a single process and does not modify the underlying mount namespace, clever use of file descriptors or directory traversal (such as multiple cd.. commands combined with nested chroot calls) can allow a malicious process to break out and view the host filesystem.34
Nsjail circumvents this by utilizing pivot_root within a new Mount Namespace (CLONE_NEWNS). Instead of superficially hiding the host directory, pivot_root fundamentally swaps the root mount of the namespace with a new, isolated filesystem—typically an ephemeral, read-only tmpfs containing only the execution binaries—and explicitly unmounts the old host root. Once the old root is unmounted, there is no mathematical or functional pathway back to the host filesystem, entirely eliminating directory traversal escapes.34
4.2.2. Resource Limitation via cgroups v2
To prevent denial-of-service attacks via resource starvation, Sentinel leverages Control Groups version 2 (cgroups v2). Unlike cgroups v1, which utilized multiple disjoint and often conflicting hierarchies for different resource controllers (CPU, memory, blkio), cgroups v2 unifies all controllers under a single, simplified hierarchy, preventing complex delegation vulnerabilities.38
When the Go worker invokes nsjail, it dynamically generates a unique cgroup for the payload, enforcing rigid constraints:
Memory Restrictions: By writing limits to memory.max, the kernel's Out-Of-Memory (OOM) killer will immediately terminate the payload if it attempts to allocate excessive RAM, protecting the worker node's stability.31
Process Limitations: Writing to pids.max explicitly limits the number of sub-processes the payload can spawn, mathematically neutralizing fork bombs before they can consume process table entries.31
CPU Quotas: Constraints on cpu.max prevent infinite loops from monopolizing processing cycles, guaranteeing fair resource allocation across concurrent executions.
4.2.3. System Call Mitigation with seccomp-bpf and Kafel
The deepest layer of isolation involves intercepting malicious system calls. Nsjail integrates with the seccomp-bpf (Secure Computing with Berkeley Packet Filters) subsystem. Writing raw BPF assembly is error-prone; therefore, Sentinel uses Kafel, a policy language developed by Google, to define explicit allowlists for system calls.31
A C++ or Python execution policy permits only benign operations such as read, write, openat, close, brk, mmap, and exit_group. Any attempt by the untrusted payload to execute dangerous system calls—such as execve (attempting to spawn a shell), ptrace (attempting to attach to and read the memory of host processes), or socket (attempting to establish unauthorized network connections)—results in an immediate SECCOMP_RET_KILL action, instantly terminating the process at the kernel level before the operation can execute.41
5. Execution Worker Architecture (Go Best Practices)
The Consumer Layer, comprised of the execution workers, is written in Go. Go's lightweight concurrency model (goroutines) and robust standard library make it exceptionally suited for high-throughput, network-bound, and process-management applications.
5.1. Goroutine Worker Pools and Concurrency
A naive implementation might spawn a new goroutine for every message received from RabbitMQ. Under heavy load, this unbounded concurrency leads to massive memory consumption, scheduler thrashing, and potential goroutine leaks if network connections stall.44
Sentinel implements a rigid fixed-size worker pool pattern. A single dispatcher goroutine connects to RabbitMQ and consumes messages over an AMQP channel. These messages are pushed into a buffered Go channel. A predetermined number of worker goroutines—calibrated to the available CPU cores of the Kubernetes pod—read from this channel concurrently. This architecture ensures that the pod never exceeds its computational capacity and that memory allocation remains predictable and garbage-collection friendly.44
5.2. Clean Architecture in Go
To maintain code hygiene and allow for rigorous unit testing without requiring active RabbitMQ or PostgreSQL instances, the Go worker codebase adheres to Clean Architecture principles.47 The project structure is logically segmented:
Domain Layer (internal/domain): Contains the core business structs, such as Job, Result, and Limits, entirely agnostic of external frameworks.
Repository Layer (internal/repository): Defines interfaces for state management (e.g., UpdateJobStatus) and implements them using the pgx library for PostgreSQL and go-redis for caching.48
Usecase Layer (internal/usecase): Houses the orchestration logic, dictating the flow from code compilation to database updating.
Delivery Layer (internal/delivery): Manages the RabbitMQ AMQP connection, transforming incoming JSON payloads into Domain structs and pushing them to the worker pool.47
By utilizing interfaces and dependency injection, developers can instantly swap the PostgreSQL repository with an in-memory mock repository during CI/CD pipelines, accelerating test execution.
5.3. Single Process Multi-Worker vs. Multiple Pods
An architectural dilemma exists regarding the deployment of the execution layer: is it better to deploy a single worker pod running 100 concurrent goroutines, or 100 worker pods each running a single process?
Deploying 100 separate pods incurs massive overhead. The Kubernetes control plane struggles with rapid churning of thousands of pods, and the networking layer (kube-proxy/CoreDNS) becomes saturated assigning IP addresses and routing AMQP traffic.46 Conversely, a single pod with a multi-worker goroutine pool shares a single AMQP multiplexed TCP connection, significantly reducing network overhead and memory consumption. Because nsjail natively isolates each execution into its own distinct kernel namespaces, running multiple simultaneous nsjail processes within a single Go pod is mathematically secure and highly efficient. Therefore, Sentinel uses horizontally scaled pods (managed by KEDA), where each pod internally runs a multi-worker goroutine pool.
5.4. Integrating os/exec with nsjail and Process Group Management
Invoking nsjail from Go requires utilizing the os/exec package. Managing the lifecycle of this subprocess is perilous. If a worker uses exec.Command and attempts to terminate a misbehaving payload via cmd.Process.Kill(), it will only send a SIGKILL to the immediate nsjail parent process.50 Because nsjail spawns further child processes inside its isolated namespaces to execute the actual payload, killing the parent leaves orphaned user-code processes running invisibly in the background, permanently leaking memory and CPU.51
To guarantee absolute resource cleanup, the Go worker utilizes Linux Process Groups. By modifying the SysProcAttr struct before starting the command (cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}), the Go runtime instructs the kernel to execute setpgid() between the fork and execve calls.53 This assigns the nsjail instance and all its subsequent children to a completely new Process Group ID (PGID). When the Go worker needs to enforce a timeout, it sends a SIGKILL to the negative value of the PID (syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)). The Linux kernel interprets this negative integer as an instruction to atomically obliterate the entire process group, ensuring that every nested child spawned by the untrusted code is instantly eradicated.53
5.5. Context Cancellation and Timeout Enforcement
Every job pulled from the queue is wrapped in a context.WithTimeout.44 This context governs the entire execution pipeline. The worker uses a select statement to monitor the context:

Go


select {
case <-ctx.Done():
    // Timeout reached: trigger process group destruction
    syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
    repository.UpdateStatus(jobID, "TIMEOUT")
case err := <-executionDoneChannel:
    // Process completed naturally
}


If the context expires, the deferred cleanup functions execute immediately, purging the ephemeral tmpfs mounts, updating the database status, and acknowledging the RabbitMQ message to prevent indefinite requeuing.44 Furthermore, all worker functions utilize deferred recover() blocks to gracefully catch and log any Go panics, ensuring a single anomalous execution cannot crash the entire worker pod.44
6. Database Design and State Management
The PostgreSQL database acts as the central nervous system of Project Sentinel, durably recording the state transitions of millions of daily executions. Traditional schema designs rapidly deteriorate under the concurrent write throughput generated by thousands of asynchronous workers updating job statuses simultaneously.56
6.1. PostgreSQL Schema and Indexing Strategy
The primary table, execution_jobs, contains columns for job_id (UUIDv7 primary key), user_id, language, code_payload, status, standard_output, and timestamps (created_at, updated_at).
Because UUIDv7 is chronologically sequential, inserting new rows appends data continuously to the end of the B-tree index, completely eliminating the massive page fragmentation and index rebalancing overhead associated with random UUIDv4 generation.57 To facilitate rapid status queries for the API gateway, a partial B-tree index is applied specifically to rows where the status is active (CREATE INDEX idx_active_jobs ON execution_jobs(job_id) WHERE status IN ('QUEUED', 'RUNNING')). This ensures the index remains exceptionally small and fits entirely within the database server's RAM.59
6.2. Partitioning Strategy
As millions of executions accumulate, the table size will exceed physical memory, degrading performance. Sentinel utilizes PostgreSQL Native Range Partitioning based on the created_at timestamp, automatically creating new partitions on a daily or weekly cadence.6
When the system needs to archive historical execution data, administrators can execute ALTER TABLE execution_jobs DETACH PARTITION jobs_january. This operation updates system catalogs in milliseconds, entirely avoiding the massive transaction log generation, table locking, and VACUUM overhead caused by running bulk DELETE FROM execution_jobs WHERE created_at < '...' queries.6
6.3. Row Locking: The Power of SKIP LOCKED
In scenarios where workers must poll the database directly (or during fallback operations if RabbitMQ experiences degradation), concurrent workers executing UPDATE execution_jobs SET status = 'RUNNING' WHERE status = 'QUEUED' will cause catastrophic lock contention. Multiple workers will attempt to lock the same row, forcing the PostgreSQL lock manager into slow-path shared memory locking, stalling the entire system.60
While advisory locks offer an alternative, managing session-based advisory locks across connection poolers like PgBouncer is highly complex and error-prone.62 Instead, Sentinel relies on the SELECT FOR UPDATE SKIP LOCKED clause.64 When a worker queries for a job, SKIP LOCKED instructs the PostgreSQL engine to immediately bypass any row currently locked by another transaction, instantaneously finding the next available unlocked row without waiting.64 This transforms a standard relational database into a highly concurrent, lock-free queueing system capable of supporting extreme parallelism.66
7. Queue Design, Backpressure, and Exactly-Once Semantics
7.1. RabbitMQ Configuration and Dead Lettering
RabbitMQ is configured to maximize durability and manage failure. Messages pushed to the execution_tasks queue are marked as persistent, forcing them to be written to disk to survive broker restarts.
If a worker pulls a message but encounters an unrecoverable internal infrastructure error (e.g., the nsjail binary is missing), it issues a basic.nack (Negative Acknowledge) with requeue=false. RabbitMQ is configured with a Dead Letter Exchange (DLX). The rejected message is automatically routed to a dead_letter_queue where it can be inspected by administrators, preventing poisoned payloads from infinitely looping and crashing the entire worker fleet.12
7.2. Enforcing Exactly-Once Delivery with Redis
Message brokers inherently guarantee at-least-once delivery. Network partitions, consumer crashes before acknowledgment, or misconfigured timeouts will inevitably result in RabbitMQ redelivering a message to a new worker.67 While this ensures no request is lost, processing a computationally expensive C++ compilation multiple times wastes critical resources.
Sentinel achieves exactly-once processing semantics by combining at-least-once delivery with strict idempotency enforced via Redis.19 When a worker receives a job, it executes a Redis command: ZADD processing_locks NX <timestamp> <job_id>.69 The NX (Not eXists) flag guarantees the operation only succeeds if the key is entirely novel. If Redis returns a 1, the worker has secured the distributed lock and proceeds to invoke nsjail. If Redis returns a 0, the worker immediately recognizes a duplicate message, skips the execution phase, and issues an ACK to RabbitMQ to permanently discard the redundant task.7
8. Kubernetes Production Architecture
Deploying Sentinel to a production environment requires a sophisticated Kubernetes architecture to ensure that the massive load spikes characteristic of competitive programming contests are handled gracefully while maintaining strict security boundaries.
8.1. Node Pool Separation and Security Taints
Executing arbitrary code on the same physical host that runs the PostgreSQL database or the internal RabbitMQ cluster is an unacceptable security risk.70 Sentinel dictates the deployment of dedicated Kubernetes Node Pools.
The nodes designated for the Execution Workers are isolated using Kubernetes Taints. By applying the taint workload=untrusted:NoSchedule to the worker nodes, the Kubernetes scheduler is explicitly forbidden from placing standard API, Database, or Broker pods on those machines.71 Only the Execution Worker deployment manifests are configured with the corresponding toleration, ensuring they are exclusively scheduled onto the isolated hardware. This topology ensures that even if a highly sophisticated zero-day exploit breaches the nsjail sandbox, the attacker compromises an ephemeral worker node completely devoid of long-term database credentials or internal cloud architecture metadata.70
8.2. Queue-Based Autoscaling utilizing KEDA
Standard Kubernetes Horizontal Pod Autoscalers (HPA) scale resources based on lagging metrics like CPU or memory utilization. For an event-driven RCE engine, this is disastrous. If 50,000 tasks are suddenly dumped into RabbitMQ, CPU-based scaling will wait until the existing workers are completely saturated before slowly spinning up new pods, resulting in massive processing delays.74
Kubernetes Event-driven Autoscaling (KEDA) solves this by proactively intercepting the custom metrics API. Sentinel deploys a ScaledObject that directly polls the RabbitMQ management API to monitor the exact queue depth:

YAML


apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: rabbitmq-worker-scaler
spec:
  scaleTargetRef:
    name: sentinel-worker-deployment
  minReplicaCount: 5
  maxReplicaCount: 200
  triggers:
    - type: rabbitmq
      metadata:
        queueName: execution_tasks
        mode: QueueLength
        value: "15"


This configuration dictates that for every 15 pending messages in the queue, KEDA mathematically instructs the HPA to provision an additional worker pod.75 During a contest spike, the system instantly scales out to hundreds of pods to consume the backlog, and dynamically scales down to a minimal footprint during idle periods, perfectly optimizing cloud infrastructure expenditures.5
9. Observability Architecture
Comprehensive observability is mandatory to detect subtle architectural degradation, such as slow memory leaks in the Go workers or anomalous sandbox rejections. Sentinel embeds Prometheus metrics clients directly within the Go applications to expose telemetry data at a /metrics endpoint, which is subsequently scraped and visualized via Grafana.79
9.1. Prometheus Metric Design Best Practices
A critical trap in Prometheus design is generating metrics with unbounded cardinality, such as attaching a unique user_id or job_id as a label to an execution metric. This exponentially multiplies the time-series arrays and will crash the Prometheus server via OOM exhaustion.79 Sentinel restricts labels strictly to bounded sets, such as language (python, cpp), status (success, failure), and queue_name.
Key instrumentations include:
Execution Latency (Duration): Measured utilizing Prometheus Histograms (histogram_quantile(0.95, rate(celery_task_duration_seconds_bucket[5m]))). This allows engineers to visualize the 95th percentile completion time of code executions, isolating anomalous slow-downs that average metrics would obscure.82
Worker Utilization: Gauges tracking the number of active versus idle goroutines within the worker pools, providing insights into whether the thread pool size aligns with pod CPU limits.83
Sandbox Efficacy: Counters tracking sentinel_sandbox_failures_total. A sudden spike indicates a systemic infrastructure failure (e.g., misconfigured cgroups or a missing library) rather than a syntax error in the user's code.
Alerting rules are configured with strict time tolerances (e.g., FOR 5m) to prevent alert fatigue triggered by transient, self-resolving network hiccups.74
10. Failure Mode Analysis
Distributed systems must be designed under the assumption that hardware and software will fail unpredictably. Project Sentinel's architecture incorporates autonomous recovery pathways for every catastrophic scenario.
Worker Pod Crashes: If a worker pod crashes mid-execution due to a kernel panic or forced Kubernetes eviction, the TCP socket to RabbitMQ is severed. RabbitMQ instantly detects the absence of the AMQP heartbeat, marks the unacknowledged message as pending, and requeues it. A healthy worker subsequently pulls and processes the task, ensuring zero request loss.67
RabbitMQ Node Crashes: Because Quorum Queues replicate data across a minimum of three nodes using the Raft algorithm, the loss of a broker node simply triggers an automatic leader election that resolves in milliseconds. Publishers (the API Gateway) experience momentary TCP backpressure but no data is lost.15
PostgreSQL Failures: Supabase operates with synchronous streaming replication. If the primary database instance fails, a standby replica is promoted. Transient connection failures within the Go API or Workers trigger exponential backoff retry loops, preventing thundering herd scenarios upon database recovery.
Underlying Node Crashes: If a physical cloud instance dies, Kubernetes immediately reschedules the tainted worker pods to healthy nodes within the cluster. Because the jobs running on the dead node were never acknowledged in RabbitMQ, they safely await processing by the newly spawned pods.
11. Development Roadmap and Troubleshooting
The systematic construction of Project Sentinel requires a disciplined, multi-phase development lifecycle to isolate variables and simplify debugging.
Phase 1: Sandbox Development (Core eBPF & nsjail): Development begins entirely outside of Go or Docker. Engineers must compile nsjail natively, write strictly locked-down Kafel policies for C++ and Python, and execute payloads directly from the Linux command line.33 This phase validates that UTS, PID, and Mount namespace isolation is functioning and that cgroups v2 memory limits successfully trigger OOM kills on abusive scripts.26
Phase 2: Decoupling & Messaging (Go + RabbitMQ): Develop the stateless Go Gin API gateway and a basic Go worker. Establish the AMQP 0-9-1 connections, implement the consumer prefetch constraints, and ensure payloads successfully transit the broker.12
Phase 3: State Management (PostgreSQL & Redis): Integrate the pgx driver. Implement the UUIDv7 generation, the FOR UPDATE SKIP LOCKED logic for database state transitions, and the Redis ZADD NX distributed locks for message deduplication.65
Phase 4: Packaging & Dockerization: Containerize the applications. The Worker Dockerfile must install the compiled nsjail binary. Crucially, the worker container must be granted specific capabilities (or run as privileged) only at the container runtime level so nsjail possesses the permissions required to spawn nested namespaces internally.
Phase 5: Scaling & Load Testing (Kubernetes + KEDA): Deploy the manifests to the cluster. Apply the Node Taints, configure the KEDA ScaledObject pointing to RabbitMQ, and utilize load-testing tools to simulate 10,000 concurrent submissions, tuning Prometheus alerts to monitor system strain.
11.1. Troubleshooting Minikube on Arch Linux
During the development phase, engineers running Minikube on Arch Linux frequently encounter virtualization barriers. Using the kvm2 driver is highly recommended over Docker for realistic cgroup testing, but it requires specific system configurations.84
Permission Denied Errors: The developer must be appended to the libvirt group to avoid recurring root password prompts and permission denials during Minikube initialization (usermod -aG libvirt $USER).85
Cgroups v2 Delegation: Arch Linux natively defaults to systemd cgroups v2.86 The Minikube QEMU/KVM instance must be verified using the virt-host-validate command. The output must show a PASS for the cgroup 'cpu', 'memory', and 'pids' controllers, ensuring they are properly delegated to the non-root user running the virtual machine, which is an absolute prerequisite for nsjail to function inside the local cluster.84
12. Recommended Final Sentinel Architecture
The finalized Project Sentinel architecture represents a hardened, asynchronous, and linearly scalable platform. It successfully isolates the extreme dangers of arbitrary code execution while providing the low latency and high availability required by competitive platforms.
12.1. System Execution Flow
Ingestion: An end-user submits code via HTTPS. The Nginx Ingress load balancer routes the request to a stateless Go API pod.
Initialization: The API validates the payload, generates a UUIDv7, inserts a QUEUED record into the partitioned PostgreSQL table, publishes the payload to RabbitMQ, and returns a 202 Accepted to the user instantly.
Task Distribution: The RabbitMQ Quorum Queue safely holds the message. A KEDA-scaled Go Worker pod pulls the message, strictly respecting a prefetch count of 1.
Deduplication: The worker executes a Redis ZADD NX command. If successful, processing begins; if not, the duplicate message is acknowledged and discarded.
Sandboxing: The Go worker writes the code payload to an ephemeral tmpfs directory, spawns a new Process Group via os/exec, and invokes nsjail with the specified Kafel system call policy and cgroups limitations. The worker simultaneously pushes a RUNNING status to Postgres using SKIP LOCKED.
Finalization: Upon process completion—or destruction via context cancellation timeouts—the standard output is captured. The worker pushes the SUCCESS or FAILED state to Postgres, unmounts the ephemeral files, and ACKs the message in RabbitMQ, freeing itself for the next task.
12.2. Component Architecture Diagram

Code snippet


+-----------------------------------------------------------------------------------+

| EXTERNAL USER (HTTPS/WSS) |
+-----------------------------------------------------------------------------------+
|
                                        v
+-----------------------------------------------------------------------------------+

| NGINX INGRESS LOAD BALANCER |
| (Rate Limiting & TLS Termination) |
+-----------------------------------------------------------------------------------+
|
                                        v
+-----------------------------------------------------------------------------------+

| API DEPLOYMENT |
| +----------------+        +----------------+        +----------------+ |
| | Go/Gin API Pod | | Go/Gin API Pod | | Go/Gin API Pod | |
| +-------+--------+        +-------+--------+        +-------+--------+ |
+----------|-------------------------|-------------------------|--------------------+

| | |
    (Push via AMQP) | (DB Write via pgx)
           v | v
+-------------------------------+ | +-----------------------------------------+

| RABBITMQ CLUSTER | | | POSTGRESQL (SUPABASE) |
| (Quorum Queues, DLX) | | | (Partitioned Tables, SKIP LOCKED) |
+---------------+---------------+ | +----------------+------------------------+

| | ^
      (Pull via Prefetch=1) | (DB Update)
                v | |
+-----------------------------------------------------------------------------------+

| KUBERNETES WORKER NODE POOL |
| (Tainted: workload=untrusted:NoSchedule) |
| |
| +-----------------------------------------------------------------------------+ |
| | KEDA HORIZONTAL POD AUTOSCALER (Scales pods based on RabbitMQ queue depth) | |
| +-----------------------------------------------------------------------------+ |
| |
| +--------------------------+                 +--------------------------+ |
| | GO WORKER POD | | GO WORKER POD | |
| | +--------------------+ | | +--------------------+ | |
| | | Clean Arch Logic |-------(ZADD)--------->| REDIS CLUSTER | | |
| | +---------+----------+ | | | (Deduplication) | | |
| | | (os/exec) | | | +--------------------+ | |
| | +---------v----------+ | | +---------v----------+ | |
| | | nsjail process | | | | nsjail process | | |
| | | (seccomp, cgroups) | | | | (seccomp, cgroups) | | |
| | +--------------------+ | | +--------------------+ | |
| +--------------------------+                 +--------------------------+ |
+-----------------------------------------------------------------------------------+
|
                                        v
+-----------------------------------------------------------------------------------+

| PROMETHEUS & GRAFANA OBSERVABILITY |
| (Scraping /metrics from API, Workers, RabbitMQ, Redis, and Postgres) |
+-----------------------------------------------------------------------------------+


By decoupling payload ingestion from execution via durable message brokering, strictly isolating untrusted arbitrary code using advanced Linux kernel namespace manipulations, and orchestrating massive dynamic horizontal scale through Kubernetes and KEDA, Project Sentinel presents a world-class, fault-tolerant architecture capable of supporting enterprise-grade technical workloads safely at massive scale.
Works cited
My Experience Building a Leetcode-Like Online Judge and How You Can Build One, accessed February 20, 2026, https://imehboob.medium.com/my-experience-building-a-leetcode-like-online-judge-and-how-you-can-build-one-7e05e031455d
Leetcode code execution system design (With working code). | by Yash Budukh - Medium, accessed February 20, 2026, https://medium.com/@yashbudukh/building-a-remote-code-execution-system-9e55c5b248d6
Remote Code Execution Engine. I have attempted to build a simple… | by Aayush Pagare | Operations Research Bit | Medium, accessed February 20, 2026, https://medium.com/operations-research-bit/remote-code-execution-engine-432c86b78ab1
Design a Coding Platform Like LeetCode | Hello Interview System Design in a Hurry, accessed February 20, 2026, https://www.hellointerview.com/learn/system-design/problem-breakdowns/leetcode
Kubernetes Event Driven Autoscaling: Spring Boot & RabbitMQ - DEV Community, accessed February 20, 2026, https://dev.to/aissam_assouik/kubernetes-event-driven-autoscaling-21ii
Documentation: 18: 5.12. Table Partitioning - PostgreSQL, accessed February 20, 2026, https://www.postgresql.org/docs/current/ddl-partitioning.html
RabbitMQ message deduplication - Medium, accessed February 20, 2026, https://medium.com/@pvladmq/rabbitmq-message-deduplication-3ab49f8519dc
System Design — Online Judge with Data Modelling | by Sai Sandeep Mopuri | Medium, accessed February 20, 2026, https://medium.com/@saisandeepmopuri/system-design-online-judge-with-data-modelling-40cb2b53bfeb
An Architectural Deep Dive: A Comparative Analysis of Kafka, RabbitMQ, and NATS - Uplatz, accessed February 20, 2026, https://uplatz.com/blog/an-architectural-deep-dive-a-comparative-analysis-of-kafka-rabbitmq-and-nats/
Which one to use Kafka, Rabbit or NATS at the backend with predominant number of Go lang Microservices : r/golang - Reddit, accessed February 20, 2026, https://www.reddit.com/r/golang/comments/17aiudy/which_one_to_use_kafka_rabbit_or_nats_at_the/
Compare NATS - NATS Docs - NATS.io, accessed February 20, 2026, https://docs.nats.io/nats-concepts/overview/compare-nats
Ten Benefits of AMQP 1.0 Flow Control - RabbitMQ, accessed February 20, 2026, https://www.rabbitmq.com/blog/2024/09/02/amqp-flow-control
Part 1: RabbitMQ Best Practices - CloudAMQP, accessed February 20, 2026, https://www.cloudamqp.com/blog/part1-rabbitmq-best-practice.html
Why do we need message brokers like RabbitMQ over a database like PostgreSQL?, accessed February 20, 2026, https://stackoverflow.com/questions/13005410/why-do-we-need-message-brokers-like-rabbitmq-over-a-database-like-postgresql
RabbitMQ Performance Optimization | by Jawad Zaarour - Medium, accessed February 20, 2026, https://medium.com/@zjawad333/rabbitmq-performance-optimization-ef2ab64a0490
Flow Control | RabbitMQ, accessed February 20, 2026, https://www.rabbitmq.com/docs/flow-control
How to Handle RabbitMQ Consumer Prefetch, accessed February 20, 2026, https://oneuptime.com/blog/post/2026-01-24-handle-rabbitmq-consumer-prefetch/view
System Design for a Competitive Coding Platform (LeetCode, HackerRank, Codeforces Style) - My App Store - A blog by Anup Sharma, accessed February 20, 2026, https://myappstore.org.in/blog/anup-sharma/system-design-for-a-competitive-coding-platform-leetcode-hackerrank-codeforces-style/
How to Process Jobs Exactly Once in Redis | Svix Resources, accessed February 20, 2026, https://www.svix.com/resources/redis/exactly-once-jobs/
Unleash the Coder Within: Hosting Your Own LeetCode with Judge0! | by Akshat Gupta, accessed February 20, 2026, https://medium.com/@writetoxyte/unleash-the-coder-within-hosting-your-own-leetcode-with-judge0-31009e09aa56
Judge0 CE - API Docs, accessed February 20, 2026, https://ce.judge0.com/
Judge0 - Code Execution Made Simple for Every Business, accessed February 20, 2026, https://judge0.com/
judge0/judge0: Robust, fast, scalable, and sandboxed open ... - GitHub, accessed February 20, 2026, https://github.com/judge0/judge0
Design LeetCode Online Judge | System Design Interview Question, accessed February 20, 2026, https://systemdesignschool.io/problems/leetcode/solution
DOMjudge programming contest jury system - GitHub, accessed February 20, 2026, https://github.com/DOMjudge/domjudge
Security and process isolation | Windmill, accessed February 20, 2026, https://www.windmill.dev/docs/advanced/security_isolation
Security of untrusted Docker containers, accessed February 20, 2026, https://security.stackexchange.com/questions/259270/security-of-untrusted-docker-containers
Kata Containers vs Firecracker vs gvisor : r/docker - Reddit, accessed February 20, 2026, https://www.reddit.com/r/docker/comments/1fmuv5b/kata_containers_vs_firecracker_vs_gvisor/
Firecracker vs gVisor: Which isolation technology should you use? | Blog - Northflank, accessed February 20, 2026, https://northflank.com/blog/firecracker-vs-gvisor
Comparison of various runtimes in Kubernetes - High-Performance Storage [HPS], accessed February 20, 2026, https://hps.vi4io.org/_media/teaching/autumn_term_2023/stud/scap_jule_anger.pdf
google/nsjail: A lightweight process isolation tool that utilizes Linux namespaces, cgroups, rlimits and seccomp-bpf syscall filters, leveraging the Kafel BPF language for enhanced security. - GitHub, accessed February 20, 2026, https://github.com/google/nsjail
refs/heads/main - platform/external/nsjail - Git at Google - Android GoogleSource, accessed February 20, 2026, https://android.googlesource.com/platform/external/nsjail/+/refs/heads/main
2021-05-21-run-python-in-a-sandbox-with-nsjail - Kruzenshtern Lab - Obsidian Publish, accessed February 20, 2026, https://publish.obsidian.md/kruzenshtern/writings/2021-05-21-run-python-in-a-sandbox-with-nsjail
Anyone want to chime in on why pivot_root is preferable to a chroot jail? It's k... | Hacker News, accessed February 20, 2026, https://news.ycombinator.com/item?id=23167383
What's the difference between chroot and pivot_root? - Stack Overflow, accessed February 20, 2026, https://stackoverflow.com/questions/68667003/whats-the-difference-between-chroot-and-pivot-root
How strong is the Firejail sandbox? #6466 - GitHub, accessed February 20, 2026, https://github.com/netblue30/firejail/discussions/6466
Modern Containers Don't Use chroot (Updated) - Robusta, accessed February 20, 2026, https://home.robusta.dev/blog/containers-dont-use-chroot
About cgroup v2 - Kubernetes, accessed February 20, 2026, https://kubernetes.io/docs/concepts/architecture/cgroups/
Exploring Cgroups v1 and Cgroups v2: Understanding the Evolution of Resource Control, accessed February 20, 2026, https://dohost.us/index.php/2025/10/11/exploring-cgroups-v1-and-cgroups-v2-understanding-the-evolution-of-resource-control/
How does cgroups v2 impact Java, .NET, and Node.js in OpenShift 4? | Red Hat Developer, accessed February 20, 2026, https://developers.redhat.com/articles/2025/11/27/how-does-cgroups-v2-impact-java-net-and-nodejs-openshift-4
Kafel | A language and library for specifying syscall filtering policies. - Google, accessed February 20, 2026, https://google.github.io/kafel/
I created a python seccomp sandbox, but per-module in your code. - Reddit, accessed February 20, 2026, https://www.reddit.com/r/Python/comments/13ql1mc/i_created_a_python_seccomp_sandbox_but_permodule/
Nsjail - Abilian Innovation Lab, accessed February 20, 2026, https://lab.abilian.com/Tech/Cloud/Nsjail/
Go Concurrency Mastery: Preventing Goroutine Leaks with Context, Timeout & Cancellation Best Practices - Dev.to, accessed February 20, 2026, https://dev.to/serifcolakel/go-concurrency-mastery-preventing-goroutine-leaks-with-context-timeout-cancellation-best-1lg0
Effective way to cleaning up long running workers : r/golang - Reddit, accessed February 20, 2026, https://www.reddit.com/r/golang/comments/1k0rtfx/effective_way_to_cleaning_up_long_running_workers/
Clean Architecture in Go: what works best for you? : r/golang - Reddit, accessed February 20, 2026, https://www.reddit.com/r/golang/comments/1lx35no/clean_architecture_in_go_what_works_best_for_you/
Clean Architecture in Go: A Practical Guide with go-clean-arch - DEV Community, accessed February 20, 2026, https://dev.to/leapcell/clean-architecture-in-go-a-practical-guide-with-go-clean-arch-51h7
Clean Architecture in Go [2024 Updated] | Panayiotis Kritiotis, accessed February 20, 2026, https://pkritiotis.io/clean-architecture-in-golang/
My Clean Architecture Go Application | by Denis Sazonov - Medium, accessed February 20, 2026, https://medium.com/@sadensmol/my-clean-architecture-go-application-e4611b1754cb
Kill child processes - Google Groups, accessed February 20, 2026, https://groups.google.com/g/golang-nuts/c/nayHpf8dVxI
Everything You Need To Know About Managing Go Processes - HackerNoon, accessed February 20, 2026, https://hackernoon.com/everything-you-need-to-know-about-managing-go-processes
Managing Go Processes | Keploy Blog, accessed February 20, 2026, https://keploy.io/blog/technology/managing-go-processes
Killing a child process and all of its children in Go | by Felix Geisendörfer - Medium, accessed February 20, 2026, https://medium.com/@felixge/killing-a-child-process-and-all-of-its-children-in-go-54079af94773
Killing child process on timeout in Go code - Getting Help - Go Forum, accessed February 20, 2026, https://forum.golangbridge.org/t/killing-child-process-on-timeout-in-go-code/995
How to Gracefully Cancel Long-Running Goroutines with Context in Go - OneUptime, accessed February 20, 2026, https://oneuptime.com/blog/post/2026-01-25-gracefully-cancel-goroutines-context-go/view
Managing PostgreSQL partitions with the pg_partman extension - Amazon Relational Database Service, accessed February 20, 2026, https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/PostgreSQL_Partitions.html
How to Handle High-Cardinality Data in PostgreSQL, accessed February 20, 2026, https://www.tigerdata.com/learn/how-to-handle-high-cardinality-data-in-postgresql
Tuning your PostgreSQL for High Performance | by Luis Sena - Medium, accessed February 20, 2026, https://luis-sena.medium.com/tuning-your-postgresql-for-high-performance-5580abed193d
PostgreSQL Performance Tuning and Optimization Guide - Sematext, accessed February 20, 2026, https://sematext.com/blog/postgresql-performance-tuning/
Improve PostgreSQL performance: Diagnose and mitigate lock manager contention - AWS, accessed February 20, 2026, https://aws.amazon.com/blogs/database/improve-postgresql-performance-diagnose-and-mitigate-lock-manager-contention/
Understanding Database Locks in PostgreSQL | by Aditi Mishra - Medium, accessed February 20, 2026, https://medium.com/@aditimishra_541/understanding-database-locks-in-postgresql-0392f0ab52d1
accessed February 20, 2026, https://news.ycombinator.com/item?id=35528018#:~:text=Advisory%20locks%20are%20purely%20in,errors%20all%20over%20the%20place.
Proposal: convert from session-based Advisory Locks to locked_at/locked_by columns · bensheldon good_job · Discussion #831 - GitHub, accessed February 20, 2026, https://github.com/bensheldon/good_job/discussions/831
Using FOR UPDATE SKIP LOCKED for Queue-Based Workflows without Deadlocks, accessed February 20, 2026, https://www.netdata.cloud/academy/update-skip-locked/
I'm building a message queue with Postgres. Should my consumers use LISTEN or poll the DB? : r/PostgreSQL - Reddit, accessed February 20, 2026, https://www.reddit.com/r/PostgreSQL/comments/1jsurtk/im_building_a_message_queue_with_postgres_should/
SQL Maxis: Why We Ditched RabbitMQ and Replaced It with a Postgres Queue | Hacker News, accessed February 20, 2026, https://news.ycombinator.com/item?id=35526846
Reliability Guide | RabbitMQ, accessed February 20, 2026, https://www.rabbitmq.com/docs/reliability
Message Deduplication with RabbitMQ Streams, accessed February 20, 2026, https://www.rabbitmq.com/blog/2021/07/28/rabbitmq-streams-message-deduplication
Queues with Redis without duplicate items - DEV Community, accessed February 20, 2026, https://dev.to/munawwar/queues-with-redis-without-duplicate-items-4nlh
Harden workload isolation with GKE Sandbox | GKE security - Google Cloud Documentation, accessed February 20, 2026, https://docs.cloud.google.com/kubernetes-engine/docs/how-to/sandbox-pods
Isolate your workloads in dedicated node pools | GKE security | Google Cloud Documentation, accessed February 20, 2026, https://docs.cloud.google.com/kubernetes-engine/docs/how-to/isolate-workloads-dedicated-nodes
Using Kata Containers on Azure Kubernetes Service for sandboxing containers, accessed February 20, 2026, https://www.danielstechblog.io/using-kata-containers-on-azure-kubernetes-service-for-sandboxing-containers/
GKE Sandbox should be used for untrusted workloads - Datadog Docs, accessed February 20, 2026, https://docs.datadoghq.com/security/default_rules/def-000-65v/
Seeking Advice on Prometheus & Grafana: What Metrics Do You Use for Alerts? - Reddit, accessed February 20, 2026, https://www.reddit.com/r/devops/comments/1f35eri/seeking_advice_on_prometheus_grafana_what_metrics/
Scaling Deployments, StatefulSets & Custom Resources - KEDA, accessed February 20, 2026, https://keda.sh/docs/2.19/concepts/scaling-deployments/
KEDA | RabbitMQ Queue, accessed February 20, 2026, https://keda.sh/docs/2.19/scalers/rabbitmq-queue/
Scaling Kubernetes with KEDA and RabbitMQ - Flightcrew, accessed February 20, 2026, https://www.flightcrew.io/blog/keda-rabbitmq
Dynamic Scaling of Applications with RabbitMQ and KEDA | by Thomas Lumesberger, accessed February 20, 2026, https://medium.com/@xcxwcqctcb/dynamic-scaling-of-applications-with-rabbitmq-and-keda-91334934a80c
Prometheus Best Practices: 8 Dos and Don'ts | Better Stack Community, accessed February 20, 2026, https://betterstack.com/community/guides/monitoring/prometheus-best-practices/
Monitoring Celery Workers with Flower: Your Tasks Need Babysitting - DEV Community, accessed February 20, 2026, https://dev.to/soumyajyoti-devops/monitoring-celery-workers-with-flower-your-tasks-need-babysitting-3ime
Beginner's Guide to Prometheus Metrics - Logz.io, accessed February 20, 2026, https://logz.io/learn/prometheus-metrics-guide/
5 Essential Prometheus Metrics Every Developer Should Monitor | by Vishal Gupta - Medium, accessed February 20, 2026, https://medium.com/observability-101/5-essential-prometheus-metrics-every-developer-should-monitor-c201ed920037
How to Use Celery for Distributed Task Queues, accessed February 20, 2026, https://oneuptime.com/blog/post/2025-07-02-python-celery-distributed-tasks/view
Kubernetes: Minikube with QEMU/KVM on Arch - DEV Community, accessed February 20, 2026, https://dev.to/xs/kubernetes-minikube-with-qemu-kvm-on-arch-312a
kvm2 - Minikube - Kubernetes, accessed February 20, 2026, https://minikube.sigs.k8s.io/docs/drivers/kvm2/
cgroups - ArchWiki, accessed February 20, 2026, https://wiki.archlinux.org/title/Cgroups
