import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterEach, vi } from 'vitest';
// jsdom does not implement browser layout observers or scrolling.
vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
Element.prototype.scrollTo = vi.fn();
Element.prototype.scrollIntoView = vi.fn();
afterEach(() => { cleanup(); localStorage.clear(); });
