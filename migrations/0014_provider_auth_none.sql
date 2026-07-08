-- 0014: allow providers.auth_scheme = 'none' for public no-credential APIs.
--
-- Wave 11 adds official open-data registries (GLEIF LEI, Norway Brønnøysund,
-- recherche-entreprises.api.gouv.fr) that require NO credential: the adapter's AuthDescriptor
-- carries Scheme "none" and no KeyPoolSelector, and the egress AuthInjector passes the request
-- through without a key lease. Widen the CHECK so those catalog rows can be seeded — the
-- TestSeedInputFor_AllRegistered guard keeps this list in lockstep with provider.AuthScheme.
ALTER TABLE providers DROP CONSTRAINT IF EXISTS providers_auth_scheme_check;
ALTER TABLE providers ADD CONSTRAINT providers_auth_scheme_check
    CHECK (auth_scheme IN
        ('none', 'api-key-header', 'api-key-query', 'api-key-path', 'api-key-dual-header',
         'bearer', 'basic', 'oauth2-cc'));
