// New API embedded Creative always loads boot assets from its same-origin /creative bundle.
(function () {
  var preference = { cdn: 'local', latency: 0, timestamp: Date.now(), embeddedCreative: true };
  var api = {
    selectBestCDN: function () { return Promise.resolve(preference); },
    getCDNBaseUrl: function () { return null; },
    clearCDNCache: function () {},
    reselectCDN: function () { return Promise.resolve(preference); },
    sources: [],
    config: { packageName: 'new-api-creative', embeddedCreative: true },
    embeddedCreative: true,
  };
  window.__OPENTU_CDN__ = preference;
  window.__AITU_CDN__ = preference;
  window.__OPENTU_CDN_API__ = api;
  window.__AITU_CDN_API__ = api;
})();
