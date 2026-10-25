import { defineConfig } from 'vitest/config';

export default defineConfig({
  base: './',
  server: { host: '127.0.0.1' },
  preview: { host: '127.0.0.1' },
  test: { environment: 'jsdom', setupFiles: './src/test-setup.ts', restoreMocks: true },
});
