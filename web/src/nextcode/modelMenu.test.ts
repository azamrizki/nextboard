import { describe, expect, it } from 'vitest';
import { ApiError } from '../api';
import type { ModelList } from '../lib/types';
import { menuModels, modelChange, modelChipText, modelErrorText, modelNameProblem, onlyEffort } from './modelMenu';

const claudeList: ModelList = {
  source: 'known',
  models: [
    { id: 'opus', name: 'Opus' },
    { id: 'sonnet', name: 'Sonnet' },
    { id: 'haiku', name: 'Haiku', efforts: ['low', 'medium'] },
  ],
  efforts: ['low', 'medium', 'high', 'max'],
  current: 'sonnet',
  currentEffort: 'high',
};

describe('menuModels', () => {
  it('lists the CLI’s models with what Default stands for', () => {
    const v = menuModels(claudeList, '', '');
    expect(v.defaultLabel).toBe('Default (Sonnet)');
    expect(v.models.map((m) => m.id)).toEqual(['opus', 'sonnet', 'haiku']);
    expect(v.efforts).toEqual(['low', 'medium', 'high', 'max']);
    expect(v.defaultEffortLabel).toBe('Default (high)');
  });

  it('keeps a typed model that the list does not have, and a model’s own efforts', () => {
    const v = menuModels(claudeList, 'claude-opus-5-5', '');
    expect(v.models.at(-1)).toEqual({ id: 'claude-opus-5-5', name: 'Opus 5.5' });
    expect(menuModels(claudeList, 'haiku', '').efforts).toEqual(['low', 'medium']);
    expect(menuModels(claudeList, 'haiku', 'max').efforts).toEqual(['low', 'medium', 'max']);
  });

  it('says Default alone before the list is known', () => {
    expect(menuModels(null, '', '')).toEqual({ defaultLabel: 'Default', models: [], efforts: [], defaultEffortLabel: 'Default' });
  });
});

describe('modelChange', () => {
  it('sends only what changes', () => {
    expect(modelChange({ model: 'sonnet', effort: 'high' }, { model: 'opus' }, claudeList)).toEqual({ model: 'opus' });
    expect(modelChange({ model: 'sonnet', effort: 'high' }, { effort: 'max' }, claudeList)).toEqual({ effort: 'max' });
    expect(modelChange({ model: 'sonnet', effort: 'high' }, { model: '' }, claudeList)).toEqual({ model: '' });
  });

  it('nothing when the pick is what the console has', () => {
    expect(modelChange({ model: 'opus', effort: '' }, { model: 'opus' }, claudeList)).toBeNull();
    expect(modelChange({}, { effort: '' }, claudeList)).toBeNull();
  });

  it('a model without the console’s effort takes the default effort with it', () => {
    expect(modelChange({ model: 'opus', effort: 'max' }, { model: 'haiku' }, claudeList)).toEqual({ model: 'haiku', effort: '' });
    expect(modelChange({ model: 'opus', effort: 'low' }, { model: 'haiku' }, claudeList)).toEqual({ model: 'haiku' });
  });

  it('tells an effort alone apart (it waits for the next message)', () => {
    expect(onlyEffort({ effort: 'high' })).toBe(true);
    expect(onlyEffort({ effort: '' })).toBe(true);
    expect(onlyEffort({ model: 'opus' })).toBe(false);
    expect(onlyEffort({ model: 'haiku', effort: '' })).toBe(false);
  });
});

describe('modelChipText', () => {
  it('shows the console’s choice, else what the CLI runs, with the effort', () => {
    expect(modelChipText({ model: 'opus', effort: 'high' }, 'claude-sonnet-5-5', claudeList.models)).toBe('Opus · high');
    expect(modelChipText({ model: '', effort: '' }, 'claude-sonnet-5-5')).toBe('Sonnet 5.5');
    expect(modelChipText({ model: '', effort: 'low' }, undefined)).toBe('Default model · low');
  });
});

describe('modelNameProblem', () => {
  it('takes the names the hub takes', () => {
    expect(modelNameProblem('claude-opus-5-5[1m]')).toBe('');
    expect(modelNameProblem('openai/gpt-5.5')).toBe('');
    expect(modelNameProblem(' gpt-5-codex ')).toBe('');
  });

  it('says what is wrong otherwise', () => {
    expect(modelNameProblem('')).toMatch(/Type/);
    expect(modelNameProblem('-opus')).toMatch(/letters/);
    expect(modelNameProblem('my model')).toMatch(/letters/);
    expect(modelNameProblem('x'.repeat(129))).toMatch(/128/);
  });
});

describe('modelErrorText', () => {
  it('names the hub’s refusals', () => {
    expect(modelErrorText(new ApiError(400, 'unknown model "x"', 'bad_model'))).toBe('That model or effort was refused: unknown model "x"');
    expect(modelErrorText(new ApiError(409, 'not a NextCode console', 'not_nextcode'))).toMatch(/not a NextCode chat/);
  });

  it('says the hub is too old when the route is missing', () => {
    expect(modelErrorText(new ApiError(404, 'endpoint not found'))).toMatch(/cannot switch a chat’s model yet/);
    expect(modelErrorText(new ApiError(405, 'Method Not Allowed'))).toMatch(/cannot switch a chat’s model yet/);
    expect(modelErrorText(new ApiError(404, 'session not found'))).toBe('Could not change the model: session not found');
  });
});
