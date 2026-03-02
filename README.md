# Hyphae

Hyphae is the gateway that lets a service running on an Underleaf‑managed edge node become publicly reachable on the Internet.  It sits outside the rest of the Cloudflare‑protected Underleaf backend and provides a controlled ingress point, while all the heavy‑lifting for orchestration still happens through the existing Mycelium Spine (control bus) and Mycelium Mesh (transport overlay).

## Key responsibilities of Hyphae
	•	Public TLS Gateway – Hyphae runs on a dedicated VPS with inbound ports open.  It terminates TLS for wildcard hostnames like *.underleafapp.com, so end‑users connect to Hyphae directly over HTTPS rather than through Cloudflare tunnels.
	•	Lease and Routing Manager – Hyphae keeps a table of leases, each linking a host name (e.g. hello.abcd.underleafapp.com) to a live tunnel between Hyphae and a specific node.  When the Underleaf control plane requests an exposure, Hyphae issues a lease and returns the lease ID so the node can bind it.
	•	Traffic Forwarder – For each active lease, Hyphae forwards inbound HTTP(S) traffic on the public host to the corresponding tunnel session.  It does no business logic beyond simple host‑based routing and health checks.

## How it interacts with the rest of Underleaf
	1.	Exposure creation – When a user runs ufctl deploy … --public (or sets public: true in a manifest), the Underleaf API server creates an Exposure record and asks Hyphae to issue a lease for that service.  It chooses or generates a hostname and stores the lease ID.
	2.	Control signalling via Spine – The API server publishes a exposure.bind command over the Mycelium Spine.  The command contains the deployment/service identifiers, the local port to expose, the host name and the Hyphae lease ID.
	3.	Mesh agent binds the tunnel – On the target node, the Mycelium Mesh Agent (MMA) receives the exposure.bind command from Spine.  It authenticates to Hyphae using the node’s identity, requests the lease, and establishes a persistent tunnel for that lease.  It registers the exposed service in the Mesh’s internal service discovery so other internal components can route to it.
	4.	Hyphae forwards traffic to the node – Once the tunnel is bound, Hyphae begins forwarding inbound requests for hello.abcd.underleafapp.com to the node’s local port through the tunnel.  If the exposure uses “org_jwt” mode, Hyphae also validates Auth0 tokens for each request.
	5.	Status and cleanup – The Mesh Agent reports success or failure back via Spine.  The API server updates the exposure’s status accordingly.  When the user removes the exposure or the lease expires, Hyphae tears down the host mapping and closes the tunnel.

This arrangement lets Underleaf offer a one‑command “deploy and expose” experience without punching holes in the user’s firewall or relying on Cloudflare for arbitrary user traffic.  Hyphae stays lightweight and stateless (aside from its lease table), while the server_api, UA‑K kernel and Mycelium Mesh continue to handle identity, deployments and service discovery.
