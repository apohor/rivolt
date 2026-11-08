// The attribution Open-Meteo's licence (CC BY 4.0) requires next to any weather it supplied: a link to open-meteo.com in its own wording.
// Put it wherever weather or an outside temperature from Open-Meteo shows. The full credits are at /credits.
export function WeatherCredit({ className = "text-xs text-neutral-500" }: { className?: string }) {
  return (
    <p className={className} data-weather-credit>
      <a href="https://open-meteo.com/" target="_blank" rel="noopener noreferrer" className="underline underline-offset-2 hover:text-neutral-300">
        Weather data by Open-Meteo.com
      </a>
    </p>
  );
}
