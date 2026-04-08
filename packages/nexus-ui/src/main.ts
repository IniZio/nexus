import { createApp } from "vue";
import PrimeVue from "primevue/config";
import Aura from "@primeuix/themes/aura";
import App from "./App.vue";
import i18n from "./i18n";
import "primeicons/primeicons.css";
import "./styles/main.css";

const app = createApp(App);

app.use(PrimeVue, {
  theme: {
    preset: Aura,
    options: {
      darkModeSelector: ".nx-dark", // never applied — light only
    },
  },
});
app.use(i18n);

app.mount("#app");
