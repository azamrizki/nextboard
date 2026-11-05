// Renders a QR code for a string (used for the nextboard://pair URI).

import { useEffect, useRef, useState } from 'react';
import QRCode from 'qrcode';

interface QRProps {
  value: string;
  /** Rendered size in CSS pixels. */
  size?: number;
}

export function QR({ value, size = 240 }: QRProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    // Dark modules on a white background with a quiet zone: scanners need the contrast.
    QRCode.toCanvas(canvas, value, {
      width: size * Math.min(window.devicePixelRatio || 1, 3),
      margin: 2,
      errorCorrectionLevel: 'M',
      color: { dark: '#000000', light: '#ffffff' },
    })
      .then(() => {
        // qrcode sets the CSS size to the (DPR-scaled) pixel width; restore the intended size.
        canvas.style.width = `${size}px`;
        canvas.style.height = `${size}px`;
        setError('');
      })
      .catch((err: unknown) => setError(String(err)));
  }, [value, size]);

  if (error) return <p className="field-error">Could not render the QR code: {error}</p>;
  return (
    <canvas
      ref={canvasRef}
      className="qr"
      style={{ width: size, height: size }}
      role="img"
      aria-label="Pairing QR code"
    />
  );
}
