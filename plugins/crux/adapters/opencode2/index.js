import { activation, modeContext } from '../../scripts/session-mode.mjs';

function key(id) {
  return typeof id === 'string' && id.trim() && id.length <= 256 ? `session:${id}:active` : null;
}

export default {
  id: 'crux',
  async setup(ctx) {
    await ctx.session.hook('prompt', async event => {
      const id = key(event.sessionID);
      if (!id) return;
      const change = activation(event.prompt?.text);
      if (change === true) await ctx.storage.set(id, true);
      if (change === false) await ctx.storage.remove(id);
    });
    await ctx.session.hook('context', async event => {
      const id = key(event.sessionID);
      if (id && await ctx.storage.get(id) === true) {
        event.system.push({ type: 'text', text: modeContext() });
      }
    });
  }
};
