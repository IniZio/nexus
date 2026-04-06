import config from '../opencode-nexus.json' with { type: 'json' };

import { createOpencodeNexusPlugin } from '../../index.js';

export const OpencodeNexusPlugin = createOpencodeNexusPlugin(config);
export default OpencodeNexusPlugin;
