# FFEE — Deployment Guide

This guide details how to deploy the Distributed Feature Flag Engine (FFEE) to a staging or production cloud environment.

---

## Architecture Overview

FFEE consists of stateless and stateful services. In a production environment, it is highly recommended to separate them:

```
                  ┌──────────────────────┐
                  │   Vite React UI      │ (S3 + CloudFront / Vercel / Netlify)
                  └──────────┬───────────┘
                             │ HTTPS
                  ┌──────────▼───────────┐
                  │    Go API Server     │ (AWS ECS Fargate / GCP Cloud Run)
                  └────┬───────────┬─────┘
            REST /     │           │ Read Cache
         State Sync    │           │ (HGETALL)
  ┌──────────┐         │    ┌──────▼──────┐
  │  Go SDK  │         │    │ Redis Cache │ (AWS ElastiCache / GCP Memorystore)
  └──────────┘         │    └──────▲──────┘
                       │           │ Write Cache
                  ┌────▼────┐      │ (HSET)
                  │Postgres │ ┌────┴─────┐
                  │  DB     │ │Go Worker │ (AWS ECS Fargate / GCP Cloud Run)
                  └────┬────┘ └────▲─────┘
                       │ WAL       │ CDC Events
                  ┌────▼────┐      │
                  │Debezium │──────┘ (AWS ECS / GCP GKE)
                  │ Connect │
                  └─────────┘
```

---

## Option A: Single-Node VPS Deployment (Simple & Low-Cost)

Best for staging, testing, or internal tools. Run the entire Docker Compose stack on a single Virtual Private Server (VPS) like AWS EC2, DigitalOcean, or Hetzner.

### 1. Provision a VPS
- OS: Ubuntu 22.04 LTS (recommended)
- Specs: At least **2 vCPUs and 4GB RAM** (Kafka & Debezium can be resource-heavy during startup).

### 2. Install Docker & Docker Compose
```bash
sudo apt update
sudo apt install -y docker.io docker-compose-v2
sudo systemctl enable --now docker
```

### 3. Clone Repository & Setup Environment
1. Clone your repository:
   ```bash
   git clone https://github.com/Devansh-Sahu/Distributed-Feature-Flag-Engine.git
   cd Distributed-Feature-Flag-Engine
   ```
2. Copy `.env.example` to `.env` and **change all default secrets**:
   ```bash
   cp .env.example .env
   nano .env
   ```
   *Change `POSTGRES_PASSWORD`, `REDIS_PASSWORD`, `GF_SECURITY_ADMIN_PASSWORD` to secure strings.*

### 4. Setup SSL Reverse Proxy (Caddy or Nginx)
Use **Caddy** (highly recommended because it handles Let's Encrypt SSL certificates automatically).

1. Install Caddy:
   ```bash
   sudo apt install -y debian-keyring debian-archive-keyring apt-transport-https
   curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
   curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | sudo tee /etc/apt/sources.list.d/caddy-stable.list
   sudo apt update
   sudo apt install caddy
   ```

2. Configure Caddy (`/etc/caddy/Caddyfile`):
   ```caddy
   ffee-api.yourdomain.com {
       reverse_proxy localhost:8080
   }

   ffee-ui.yourdomain.com {
       # If deploying Admin UI as static files
       root * /var/www/ffee-admin-ui
       file_server
       try_files {path} /index.html
   }
   ```

3. Restart Caddy:
   ```bash
   sudo systemctl restart caddy
   ```

### 5. Build and Run
```bash
docker compose up -d --build
```

---

## Option B: Managed Cloud Deployment (Production-Grade)

For mission-critical production environments, deploy stateful systems as managed cloud services and run stateless applications as container tasks inside a private network.

### 1. Stateful Services (Managed Databases)
- **Database**: **AWS RDS PostgreSQL** (or Google Cloud SQL for PostgreSQL).
  * You MUST enable Logical Replication for Debezium to read transactions. On AWS RDS, set the DB Parameter Group setting `rds.logical_replication = 1` and reboot the instance.
- **Cache**: **AWS ElastiCache for Redis** (or Google Cloud Memorystore).
- **Message Broker**: **AWS MSK (Managed Streaming for Kafka)** or Confluent Cloud.

### 2. Stateless Services (API Server & Worker)
Deploy the container images to **AWS ECS (Fargate)** or **Google Cloud Run**:
- Build your production images using the `Dockerfile`s in `/server` and `/worker`.
- Deploy the **API Server** behind an Application Load Balancer (ALB) exposing port `8080`.
- Deploy the **Worker** as a private service (no public IP/ports) with access to RDS, ElastiCache, and Kafka.

### 3. CDC Connector (Debezium)
- Build a container running the `debezium/connect:2.6` image.
- Register the Postgres connector by hitting the REST endpoint with your production database credentials.

### 4. Admin UI (Static CDN)
- Run `npm run build` in `/admin-ui` to output optimized static HTML/JS/CSS assets to `/dist`.
- Deploy the `/dist` directory directly to a static hosting provider or CDN:
  - **AWS S3 + CloudFront**
  - **Vercel**
  - **Netlify**
  - **Cloudflare Pages**
- Configure your custom domain and point the backend API requests to your Go API Server domain.

---

## Deployment & Security Checklist

- [ ] **Change Default Credentials**: Update PG, Redis, Kafka UI, and Grafana admin passwords.
- [ ] **VPC / Network Isolation**: Ensure PostgreSQL, Redis, and Kafka brokers are in private subnets and are **never** accessible from the public internet. Only the Go API Server and Go Worker should be allowed in their security groups.
- [ ] **SSL/TLS**: Secure all API endpoints (`/api/v1/...`) and the Admin UI with HTTPS.
- [ ] **Logical Replication Slot**: Grant the replication user the `REPLICATION` attribute in Postgres and ensure the replication slot `ffee_debezium_slot` is monitored.
- [ ] **CORS Settings**: Restrict `CORS_ALLOWED_ORIGINS` on the Go server config to only allow your hosted Admin UI domain.
