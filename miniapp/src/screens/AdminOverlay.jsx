import { Overlay } from '../ui/Overlay.jsx'
import { Section } from '../ui/Section.jsx'
import { AccessSection } from './AccessSection.jsx'
import { ParkSection } from './ParkSection.jsx'

// Администрирование: парк целиком и доступы к этому роутеру.
//
// «Парк» -- своя секция (ParkSection.jsx): там обновление агентов и
// массовые действия. Здесь -- только входы в экраны радиуса одного роутера.
export function AdminOverlay({ routerID, isAdmin = false, onClose, openSheet, onOpenAgentConfig, onOpenDNSReset, onOpenRouter }) {
  return (
    <Overlay title="Обслуживание и доступы" backLabel="Роутер" onBack={onClose}>
      <div class="screen">
        {isAdmin && <ParkSection openSheet={openSheet} onOpenRouter={onOpenRouter} currentID={routerID} />}

        {/* Настройки агента -- вход только у админа: радиус правки
            router-global, и сервер ответит остальным 404. Сам экран
            проверяет ещё и версию агента: у старого поля не рисуются. */}
        {isAdmin && onOpenAgentConfig && (
          <Section title="Настройки агента">
            <button type="button" class="btn btn-ghost btn-wide" onClick={onOpenAgentConfig}>
              Открыть настройки агента
            </button>
            <p class="hint">
              Как часто роутер отчитывается, адрес и логин его панели, что агенту разрешено делать
              с устройством. Изменение перезапускает агента.
            </p>
          </Section>
        )}

        {/* Сброс DNS -- вход только у админа (радиус router-global, сервер
            ответит остальным 404). Экран сам проверяет версию агента и
            начинает с предпросмотра. */}
        {isAdmin && onOpenDNSReset && (
          <Section title="Сброс DNS">
            <button type="button" class="btn btn-ghost btn-wide" onClick={onOpenDNSReset}>
              Открыть сброс DNS
            </button>
            <p class="hint">
              Заменить DNS-серверы роутера эталонными. Сначала экран покажет, что изменится.
            </p>
          </Section>
        )}

        <AccessSection routerID={routerID} openSheet={openSheet} />
      </div>
    </Overlay>
  )
}
