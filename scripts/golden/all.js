'use strict';
// Regenerates every golden file. See README.md.
const { execFileSync } = require('child_process');
for (const s of ['twitch.js', 'youtube.js', 'config.js', 'secrets.js', 'session.js', 'viewers.js']) {
  process.stdout.write(`golden: ${s}\n`);
  execFileSync(process.execPath, [require.resolve('./' + s)], { stdio: 'inherit' });
}
