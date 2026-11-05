// Diagram starters without AI (E3): small nb.diagram/v1 templates landed as shapes.

import type { Diagram } from '../types';

export const STARTERS: { id: string; label: string; hint: string; diagram: Diagram }[] = [
  {
    id: 'flow',
    label: 'Flow',
    hint: 'Start → work → end',
    diagram: {
      schema: 'nb.diagram/v1',
      title: '',
      direction: 'LR',
      nodes: [
        { id: 'start', label: 'Start', shape: 'ellipse' },
        { id: 'work', label: 'Do the work', shape: 'round' },
        { id: 'end', label: 'Done', shape: 'ellipse' },
      ],
      edges: [
        { from: 'start', to: 'work' },
        { from: 'work', to: 'end' },
      ],
      groups: [],
    },
  },
  {
    id: 'steps',
    label: 'Steps',
    hint: 'Four numbered steps',
    diagram: {
      schema: 'nb.diagram/v1',
      title: '',
      direction: 'TB',
      nodes: [
        { id: 's1', label: '1. First step', shape: 'box' },
        { id: 's2', label: '2. Second step', shape: 'box' },
        { id: 's3', label: '3. Third step', shape: 'box' },
        { id: 's4', label: '4. Last step', shape: 'box' },
      ],
      edges: [
        { from: 's1', to: 's2' },
        { from: 's2', to: 's3' },
        { from: 's3', to: 's4' },
      ],
      groups: [],
    },
  },
  {
    id: 'decision',
    label: 'Decision',
    hint: 'A question with two answers',
    diagram: {
      schema: 'nb.diagram/v1',
      title: '',
      direction: 'TB',
      nodes: [
        { id: 'q', label: 'Is it ready?', shape: 'diamond' },
        { id: 'yes', label: 'Ship it', shape: 'round' },
        { id: 'no', label: 'Fix and retry', shape: 'round' },
      ],
      edges: [
        { from: 'q', to: 'yes', label: 'yes' },
        { from: 'q', to: 'no', label: 'no' },
      ],
      groups: [],
    },
  },
  {
    id: 'mind',
    label: 'Mind map',
    hint: 'A topic with four branches',
    diagram: {
      schema: 'nb.diagram/v1',
      title: '',
      direction: 'LR',
      nodes: [
        { id: 'topic', label: 'Topic', shape: 'ellipse' },
        { id: 'a', label: 'Idea A', shape: 'round' },
        { id: 'b', label: 'Idea B', shape: 'round' },
        { id: 'c', label: 'Idea C', shape: 'round' },
        { id: 'd', label: 'Idea D', shape: 'round' },
      ],
      edges: [
        { from: 'topic', to: 'a' },
        { from: 'topic', to: 'b' },
        { from: 'topic', to: 'c' },
        { from: 'topic', to: 'd' },
      ],
      groups: [],
    },
  },
];
