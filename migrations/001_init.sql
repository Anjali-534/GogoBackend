-- ============================================================
-- DEPLOYKIT - Complete Database Schema
-- ============================================================

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ============================================================
-- USERS
-- ============================================================
CREATE TABLE users (
  id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  email           TEXT UNIQUE NOT NULL,
  name            TEXT NOT NULL,
  avatar_url      TEXT,
  github_id       BIGINT UNIQUE,
  github_login    TEXT,
  gitlab_id       BIGINT UNIQUE,
  gitlab_login    TEXT,
  password_hash   TEXT,
  is_verified     BOOLEAN DEFAULT FALSE,
  created_at      TIMESTAMPTZ DEFAULT NOW(),
  updated_at      TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- OAUTH TOKENS (GitHub / GitLab per user)
-- ============================================================
CREATE TABLE oauth_tokens (
  id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider      TEXT NOT NULL CHECK (provider IN ('github','gitlab')),
  access_token  TEXT NOT NULL,
  refresh_token TEXT,
  expires_at    TIMESTAMPTZ,
  scopes        TEXT[],
  created_at    TIMESTAMPTZ DEFAULT NOW(),
  updated_at    TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE(user_id, provider)
);

-- ============================================================
-- PROJECTS (top-level workspace, like a GitHub org)
-- ============================================================
CREATE TABLE projects (
  id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  name        TEXT NOT NULL,
  slug        TEXT UNIQUE NOT NULL,
  owner_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  plan        TEXT NOT NULL DEFAULT 'starter' CHECK (plan IN ('starter','standard','enterprise')),
  created_at  TIMESTAMPTZ DEFAULT NOW(),
  updated_at  TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- PROJECT MEMBERS (RBAC)
-- ============================================================
CREATE TABLE project_members (
  id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role        TEXT NOT NULL DEFAULT 'developer' CHECK (role IN ('owner','admin','developer','viewer')),
  invited_by  UUID REFERENCES users(id),
  created_at  TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE(project_id, user_id)
);

-- ============================================================
-- CLOUD CREDENTIALS (per project, per provider)
-- ============================================================
CREATE TABLE cloud_credentials (
  id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  provider        TEXT NOT NULL CHECK (provider IN ('aws','gcp','azure')),
  name            TEXT NOT NULL,
  aws_account_id  TEXT,
  aws_role_arn    TEXT,
  aws_region      TEXT,
  gcp_project_id  TEXT,
  gcp_service_account JSONB,
  azure_subscription_id TEXT,
  azure_tenant_id TEXT,
  azure_client_id TEXT,
  azure_client_secret TEXT,
  created_at      TIMESTAMPTZ DEFAULT NOW(),
  updated_at      TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- CLUSTERS (one per customer environment)
-- ============================================================
CREATE TABLE clusters (
  id                    UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  project_id            UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  cloud_credential_id   UUID REFERENCES cloud_credentials(id),
  name                  TEXT NOT NULL,
  provider              TEXT NOT NULL CHECK (provider IN ('aws','gcp','azure','local')),
  region                TEXT NOT NULL,
  k8s_version           TEXT,
  status                TEXT NOT NULL DEFAULT 'provisioning'
                          CHECK (status IN ('provisioning','active','error','deleting','deleted')),
  vanity_url            TEXT,
  ingress_ip            TEXT,
  agent_connected       BOOLEAN DEFAULT FALSE,
  agent_version         TEXT,
  last_heartbeat        TIMESTAMPTZ,
  kubeconfig            TEXT,
  infra_state           JSONB,
  node_count            INT DEFAULT 2,
  node_instance_type    TEXT DEFAULT 't3.medium',
  created_at            TIMESTAMPTZ DEFAULT NOW(),
  updated_at            TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- ENVIRONMENT GROUPS (shared env vars across apps)
-- ============================================================
CREATE TABLE env_groups (
  id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  cluster_id  UUID NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,
  version     INT NOT NULL DEFAULT 1,
  created_at  TIMESTAMPTZ DEFAULT NOW(),
  updated_at  TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE(cluster_id, name)
);

CREATE TABLE env_group_vars (
  id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  env_group_id  UUID NOT NULL REFERENCES env_groups(id) ON DELETE CASCADE,
  key           TEXT NOT NULL,
  value         TEXT NOT NULL,
  is_secret     BOOLEAN DEFAULT FALSE,
  created_at    TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE(env_group_id, key)
);

CREATE TABLE env_group_versions (
  id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  env_group_id  UUID NOT NULL REFERENCES env_groups(id) ON DELETE CASCADE,
  version       INT NOT NULL,
  snapshot      JSONB NOT NULL,
  created_by    UUID REFERENCES users(id),
  created_at    TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- APPS (web services, workers, cron jobs)
-- ============================================================
CREATE TABLE apps (
  id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  project_id        UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  cluster_id        UUID NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
  name              TEXT NOT NULL,
  type              TEXT NOT NULL DEFAULT 'web'
                      CHECK (type IN ('web','worker','cron','job')),
  status            TEXT NOT NULL DEFAULT 'deploying'
                      CHECK (status IN ('deploying','running','errored','sleeping','deleting')),
  -- Source
  repo_url          TEXT,
  repo_branch       TEXT DEFAULT 'main',
  repo_provider     TEXT CHECK (repo_provider IN ('github','gitlab','docker')),
  docker_image      TEXT,
  -- Build
  build_method      TEXT DEFAULT 'buildpack'
                      CHECK (build_method IN ('buildpack','dockerfile','image')),
  dockerfile_path   TEXT DEFAULT 'Dockerfile',
  build_context     TEXT DEFAULT '.',
  -- Runtime
  start_command     TEXT,
  port              INT DEFAULT 3000,
  cpu_millicores    INT DEFAULT 100,
  ram_mb            INT DEFAULT 256,
  replicas          INT DEFAULT 1,
  -- Autoscaling
  autoscaling_enabled BOOLEAN DEFAULT FALSE,
  min_replicas        INT DEFAULT 1,
  max_replicas        INT DEFAULT 10,
  scale_on_cpu        INT DEFAULT 80,
  -- Cron
  cron_schedule     TEXT,
  -- Networking
  is_public         BOOLEAN DEFAULT TRUE,
  custom_domain     TEXT,
  subdomain         TEXT,
  -- Health check
  health_check_path TEXT DEFAULT '/health',
  -- Timestamps
  last_deployed_at  TIMESTAMPTZ,
  created_at        TIMESTAMPTZ DEFAULT NOW(),
  updated_at        TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE(cluster_id, name)
);

-- ============================================================
-- APP ENV VARS
-- ============================================================
CREATE TABLE app_env_vars (
  id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  app_id      UUID NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  key         TEXT NOT NULL,
  value       TEXT NOT NULL,
  is_secret   BOOLEAN DEFAULT FALSE,
  created_at  TIMESTAMPTZ DEFAULT NOW(),
  updated_at  TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE(app_id, key)
);

-- Link apps to env groups
CREATE TABLE app_env_groups (
  app_id        UUID NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  env_group_id  UUID NOT NULL REFERENCES env_groups(id) ON DELETE CASCADE,
  PRIMARY KEY(app_id, env_group_id)
);

-- ============================================================
-- BUILDS
-- ============================================================
CREATE TABLE builds (
  id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  app_id        UUID NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  status        TEXT NOT NULL DEFAULT 'queued'
                  CHECK (status IN ('queued','building','success','failed','cancelled')),
  commit_sha    TEXT,
  commit_msg    TEXT,
  commit_author TEXT,
  branch        TEXT,
  image_url     TEXT,
  logs          TEXT,
  error_msg     TEXT,
  started_at    TIMESTAMPTZ,
  finished_at   TIMESTAMPTZ,
  created_at    TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- DEPLOYMENTS (revisions)
-- ============================================================
CREATE TABLE deployments (
  id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  app_id          UUID NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  build_id        UUID REFERENCES builds(id),
  revision        INT NOT NULL,
  status          TEXT NOT NULL DEFAULT 'deploying'
                    CHECK (status IN ('deploying','successful','failed','rolled_back')),
  image_url       TEXT,
  config_snapshot JSONB,
  deployed_by     UUID REFERENCES users(id),
  rollback_of     UUID REFERENCES deployments(id),
  created_at      TIMESTAMPTZ DEFAULT NOW(),
  updated_at      TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- DEPLOYMENT EVENTS (activity feed per app)
-- ============================================================
CREATE TABLE deployment_events (
  id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  app_id          UUID NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  deployment_id   UUID REFERENCES deployments(id),
  type            TEXT NOT NULL,
  message         TEXT NOT NULL,
  metadata        JSONB,
  created_at      TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- PREVIEW ENVIRONMENTS
-- ============================================================
CREATE TABLE preview_environments (
  id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  app_id      UUID NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  pr_number   INT NOT NULL,
  pr_title    TEXT,
  branch      TEXT NOT NULL,
  status      TEXT NOT NULL DEFAULT 'creating'
                CHECK (status IN ('creating','active','deleting','deleted')),
  url         TEXT,
  created_at  TIMESTAMPTZ DEFAULT NOW(),
  updated_at  TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE(app_id, pr_number)
);

-- ============================================================
-- DATABASES (managed add-ons)
-- ============================================================
CREATE TABLE managed_databases (
  id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  project_id    UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  cluster_id    UUID NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
  name          TEXT NOT NULL,
  engine        TEXT NOT NULL CHECK (engine IN ('postgres','mysql','redis','mongodb','clickhouse')),
  version       TEXT,
  status        TEXT NOT NULL DEFAULT 'creating'
                  CHECK (status IN ('creating','available','deleting','deleted')),
  connection_url TEXT,
  cpu_millicores INT DEFAULT 500,
  ram_mb         INT DEFAULT 512,
  storage_gb     INT DEFAULT 20,
  created_at    TIMESTAMPTZ DEFAULT NOW(),
  updated_at    TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- BILLING USAGE (reported to Stripe hourly)
-- ============================================================
CREATE TABLE billing_usage (
  id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  project_id    UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  period_start  TIMESTAMPTZ NOT NULL,
  period_end    TIMESTAMPTZ NOT NULL,
  cpu_millicores BIGINT DEFAULT 0,
  ram_mb         BIGINT DEFAULT 0,
  stripe_reported BOOLEAN DEFAULT FALSE,
  created_at    TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- NOTIFICATIONS
-- ============================================================
CREATE TABLE notifications (
  id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  project_id  UUID REFERENCES projects(id) ON DELETE CASCADE,
  type        TEXT NOT NULL,
  title       TEXT NOT NULL,
  message     TEXT,
  is_read     BOOLEAN DEFAULT FALSE,
  metadata    JSONB,
  created_at  TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- WEBHOOKS
-- ============================================================
CREATE TABLE webhooks (
  id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  app_id      UUID REFERENCES apps(id) ON DELETE CASCADE,
  url         TEXT NOT NULL,
  secret      TEXT NOT NULL,
  events      TEXT[] DEFAULT ARRAY['deploy.success','deploy.failed'],
  is_active   BOOLEAN DEFAULT TRUE,
  created_at  TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- INDEXES
-- ============================================================
CREATE INDEX idx_apps_cluster_id ON apps(cluster_id);
CREATE INDEX idx_apps_project_id ON apps(project_id);
CREATE INDEX idx_builds_app_id ON builds(app_id);
CREATE INDEX idx_deployments_app_id ON deployments(app_id);
CREATE INDEX idx_deployment_events_app_id ON deployment_events(app_id);
CREATE INDEX idx_clusters_project_id ON clusters(project_id);
CREATE INDEX idx_notifications_user_id ON notifications(user_id);
CREATE INDEX idx_billing_usage_project_id ON billing_usage(project_id);
CREATE INDEX idx_project_members_user_id ON project_members(user_id);
