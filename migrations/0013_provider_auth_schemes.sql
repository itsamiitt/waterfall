-- 0013: widen providers.auth_scheme for the ADR-0024 egress auth schemes.
--
-- The code's provider.AuthScheme enum gained two schemes during the 200-provider rollout —
-- 'api-key-path' (MixRank: key is a URL path segment) and 'api-key-dual-header' (Tomba,
-- PredictLeads, Adapt.io: two credential headers). The migration-0005 CHECK predated them and
-- rejected catalog rows for those providers at seed time (providers_auth_scheme_check, 23514).
-- Widen the allow-list so every adapter the registry constructs can be seeded and enabled.
ALTER TABLE providers DROP CONSTRAINT IF EXISTS providers_auth_scheme_check;
ALTER TABLE providers ADD CONSTRAINT providers_auth_scheme_check
    CHECK (auth_scheme IN
        ('api-key-header', 'api-key-query', 'api-key-path', 'api-key-dual-header',
         'bearer', 'basic', 'oauth2-cc'));
